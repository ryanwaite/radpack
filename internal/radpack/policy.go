package radpack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

func match(pattern, text string) bool { return regexp.MustCompile(pattern).MatchString(text) }

func safePath(value string) error {
	if value == "" || len(value) > 240 || match("[^\\x20-\\x7e]|[\\\\:*?\"<>|]", value) || path.IsAbs(value) {
		return fmt.Errorf("pack paths must be portable relative paths")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") ||
			match(`(?i)^(con|prn|aux|nul|com[0-9]|lpt[0-9])(\.|$)`, part) || match(`(?i)^(\.git|\.?git~[0-9]+)$`, part) {
			return fmt.Errorf("unsafe pack path: %q", value)
		}
	}
	return nil
}
func excludedReason(file string) (string, error) {
	if err := safePath(file); err != nil {
		return "", err
	}
	lower := strings.ToLower(file)
	name := path.Base(lower)
	for _, part := range strings.Split(lower, "/") {
		switch part {
		case ".rad", ".radpack", ".kube", ".aws", ".azure", ".ssh", ".docker", ".terraform", "node_modules", ".staging", "logs":
			return "local configuration, state, cache, or reserved Radpack metadata", nil
		}
		if strings.HasPrefix(part, ".staging-") {
			return "model staging files", nil
		}
	}
	if match(`^\.env(\.|$)`, name) && !match(`^\.env\.(example|sample|template)$`, name) {
		return "environment values", nil
	}
	if match(`credential|kubeconfig|\.tfstate(\.|$)|\.tfvars(\.|$)|\.bicepparam$|\.parameters\.json$|\.pem$|\.key$|^id_(rsa|ed25519|ecdsa)$|\.log$`, name) {
		return "credentials, deployment parameters, or state", nil
	}
	switch name {
	case ".npmrc", ".netrc", ".pypirc", "secrets.json", "secrets.yaml", "secrets.yml":
		return "credential-bearing configuration", nil
	case "radius-environment.bicep", "radius-recipe-pack.bicep", "app-graph.json", "radius-state", "radius-state.tar.gz":
		return "generated environment, provider pack, or graph/state output", nil
	}
	if strings.HasPrefix(lower, ".github/workflows/") {
		return "workflows are supplied by the consumer, not installed by a pack", nil
	}
	return "", nil
}

type regularFile struct {
	Bytes []byte
	Mode  string
}

func fingerprint(file *regularFile) *Fingerprint {
	if file == nil {
		return nil
	}
	return &Fingerprint{hash(file.Bytes), file.Mode}
}
func limitedRead(reader io.Reader, limit int) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > limit {
		return nil, fmt.Errorf("file or archive exceeds size limit")
	}
	return b, nil
}
func readRegular(root, file string) (*regularFile, error) {
	if err := safePath(file); err != nil {
		return nil, err
	}
	current := root
	parts := strings.Split(file, "/")
	for i, part := range parts {
		current = filepath.Join(current, part)
		stat, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if stat.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symbolic links are not allowed: %s", file)
		}
		if i < len(parts)-1 {
			if !stat.IsDir() {
				return nil, fmt.Errorf("not a directory in path: %s", file)
			}
			continue
		}
		if !stat.Mode().IsRegular() {
			return nil, fmt.Errorf("not a regular file: %s", file)
		}
		if stat.Size() > maxFileBytes {
			return nil, fmt.Errorf("file exceeds the 8 MiB limit: %s", file)
		}
		f, err := os.Open(current)
		if err != nil {
			return nil, err
		}
		b, readErr := limitedRead(f, maxFileBytes)
		closeErr := f.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		mode := "100644"
		if stat.Mode()&0111 != 0 {
			mode = "100755"
		}
		return &regularFile{b, mode}, nil
	}
	return nil, nil
}
func textOf(b []byte, file string) (string, error) {
	if bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
		return "", fmt.Errorf("only UTF-8 text payloads are supported: %s", file)
	}
	return string(b), nil
}

var contentRules = []struct{ name, pattern string }{
	{"private key", `-----BEGIN ([A-Z0-9 ]+ )?PRIVATE KEY-----`},
	{"GitHub token", `\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`},
	{"npm token", `\bnpm_[A-Za-z0-9]{20,}\b`},
	{"AWS access key", `\b(AKIA|ASIA)[A-Z0-9]{16}\b`},
	{"credential-bearing URL", `(?i)\b[a-z][a-z0-9+.-]*://[^/\s:@"']+:[^/\s@"']+@`},
	{"JWT-like token", `\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`},
	{"cloud resource binding", `(?i)(/subscriptions/[a-f0-9-]{36}/|arn:aws[a-z-]*:[a-z0-9-]+:[^:\s]*:\d{12}:)`},
	{"deployment environment resource", `(?i)['"](Radius\.Core|Applications\.Core)/environments@`},
	{"state registry binding", `(?i)ghcr\.io/[a-z0-9_.-]+/[a-z0-9_.-]*radius-state[a-z0-9_.-]*`},
	{"SAS or storage credential", `(?i)([?&]sig=[A-Za-z0-9%+/=]{12,}|AccountKey=[A-Za-z0-9+/=]{16,})`},
}
var assignments = regexp.MustCompile(`["']?([A-Za-z_][A-Za-z0-9_.-]*)["']?\s*[:=]\s*("([^"\r\n]*)"|'([^'\r\n]*)'|([^\s,}\r\n]+))`)

func inspectContent(b []byte, file string) ([]string, error) {
	text, err := textOf(b, file)
	if err != nil {
		return nil, err
	}
	findings := []string{}
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			findings = append(findings, s)
			seen[s] = true
		}
	}
	for _, rule := range contentRules {
		if match(rule.pattern, text) {
			add(rule.name)
		}
	}
	for _, m := range assignments.FindAllStringSubmatch(text, -1) {
		key, value, quoted := m[1], m[5], false
		if strings.HasPrefix(m[2], `"`) {
			value = m[3]
			quoted = true
		}
		if strings.HasPrefix(m[2], "'") {
			value = m[4]
			quoted = true
		}
		sensitive := match(`(?i)(password|passwd|client[_-]?secret|api[_-]?key|access[_-]?token|(^|[_-])(token|secret)|secret[_-]?(key|access[_-]?key)|connection[_-]?string)$`, key)
		binding := match(`(?i)^(AZURE_(CLIENT_ID|TENANT_ID|SUBSCRIPTION_ID|RESOURCE_GROUP|AKS_CLUSTER_NAME)|AWS_(ROLE_ARN|ACCOUNT_ID)|RADIUS_(STATE_REGISTRY|VPC_ID|SUBNET_IDS)|KUBERNETES_NAMESPACE|subscriptionId|tenantId|roleArn)$`, key)
		if !sensitive && !binding {
			continue
		}
		if value == "" || match(`^(null|undefined|true|false|\{|\[)$`, value) ||
			match(`^(<[^>]+>|\$\{[^}]+\}|\$\([^)]+\)|\$\{\{.+\}\})$`, value) ||
			match(`^(process\.env\.|os\.environ|env\.|secrets\.|vars\.)`, value) {
			continue
		}
		if strings.HasSuffix(file, ".bicep") && !quoted && match(`^[A-Za-z_][A-Za-z0-9_]*([.(]|$)`, value) {
			continue
		}
		if !quoted && !binding && match(`\.[A-Za-z_]`, value) {
			continue
		}
		if binding {
			add("literal deployment binding")
		} else {
			add("literal credential assignment")
		}
	}
	if match(`\bparam\s+environment\s+string\s*=`, text) {
		add("environment parameter has a default")
	}
	if match(`\benvironment\s*:\s*['"][^'"]+['"]`, text) {
		add("literal environment selection")
	}
	for _, loc := range regexp.MustCompile(`@secure\s*\(\s*\)`).FindAllStringIndex(text, -1) {
		tail := text[loc[1]:]
		if m := regexp.MustCompile(`\bparam\s+\w+\s+(\w+)(\s*=)?`).FindStringSubmatch(tail); m != nil &&
			(m[1] == "string" || m[1] == "object") && m[2] != "" {
			add("secure parameter has a default")
		}
	}
	if match(`\bkind\s*:\s*['"]?Secret\b`, text) && match(`(?m)^\s*(data|stringData)\s*:`, text) {
		add("Kubernetes Secret values")
	}
	for _, loc := range regexp.MustCompile(`\bresource\s+\w+\s+'Radius\.Security/secrets@[^']+'\s*=\s*\{`).FindAllStringIndex(text, -1) {
		body, err := resourceBody(text, loc[1]-1)
		if err != nil {
			return nil, err
		}
		if match(`\bvalue\s*:\s*['"][^'"]+['"]`, body) {
			add("literal Radius Secret value")
		}
	}
	return findings, nil
}
func resourceBody(text string, start int) (string, error) {
	depth := 0
	var quote byte
	comment := 0
	for i := start; i < len(text); i++ {
		c := text[i]
		var next byte
		if i+1 < len(text) {
			next = text[i+1]
		}
		if comment == 1 {
			if c == '\n' {
				comment = 0
			}
			continue
		}
		if comment == 2 {
			if c == '*' && next == '/' {
				comment = 0
				i++
			}
			continue
		}
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				if next == quote {
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		if c == '/' && next == '/' {
			comment = 1
			i++
			continue
		}
		if c == '/' && next == '*' {
			comment = 2
			i++
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if c == '{' {
			depth++
		}
		if c == '}' {
			depth--
			if depth == 0 {
				return text[start : i+1], nil
			}
		}
	}
	return "", fmt.Errorf("cannot inspect an unterminated Radius Secret resource")
}
func assertPortable(b []byte, file string) error {
	reason, err := excludedReason(file)
	if err != nil {
		return err
	}
	if reason != "" {
		return fmt.Errorf("cannot package %s: %s", file, reason)
	}
	findings, err := inspectContent(b, file)
	if err != nil {
		return err
	}
	if len(findings) > 0 {
		return fmt.Errorf("cannot package %s: %s. Values withheld; use consumer-supplied inputs", file, strings.Join(findings, ", "))
	}
	return nil
}
func assertPortableFile(b []byte, file string) error {
	if !strings.HasSuffix(file, ".tgz") {
		return assertPortable(b, file)
	}
	reason, err := excludedReason(file)
	if err != nil {
		return err
	}
	if reason != "" {
		return fmt.Errorf("cannot package %s: %s", file, reason)
	}
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("invalid gzip archive: %s", file)
	}
	data, readErr := limitedRead(gz, maxPackBytes)
	closeErr := gz.Close()
	if readErr != nil || closeErr != nil {
		return fmt.Errorf("invalid or oversized gzip archive: %s", file)
	}
	// Scan extended metadata and trailing bytes, not just extracted TAR members.
	scan := strings.ToValidUTF8(string(bytes.ReplaceAll(data, []byte{0}, []byte{'\n'})), "\ufffd")
	findings, err := inspectContent([]byte(scan), file)
	if err != nil {
		return err
	}
	if len(findings) > 0 {
		return fmt.Errorf("cannot package %s: %s (values withheld)", file, strings.Join(findings, ", "))
	}
	reader := tar.NewReader(bytes.NewReader(data))
	names := map[string]bool{}
	count := 0
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("invalid TAR archive: %s", file)
		}
		count++
		name := strings.TrimSuffix(strings.TrimPrefix(header.Name, "./"), "/")
		if err := safePath(name); err != nil {
			return fmt.Errorf("unsafe archive entry in %s", file)
		}
		key := strings.ToLower(name)
		reason, err := excludedReason(name)
		if err != nil || reason != "" || names[key] || count > 1000 || header.Size < 0 || header.Size > maxFileBytes ||
			(header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Typeflag != tar.TypeDir) {
			return fmt.Errorf("unsupported, duplicate, or oversized archive entry in %s", file)
		}
		names[key] = true
		member, err := limitedRead(reader, maxFileBytes)
		if err != nil {
			return fmt.Errorf("invalid archive contents: %s", file)
		}
		if header.Typeflag != tar.TypeDir {
			if err := assertPortable(member, file+"/"+name); err != nil {
				return fmt.Errorf("non-portable archive contents in %s; inspect its members (values withheld)", file)
			}
		}
	}
	if count == 0 {
		return fmt.Errorf("empty TAR archive: %s", file)
	}
	return nil
}
func dependencies(b []byte, file string) ([]string, error) {
	if !strings.HasSuffix(file, ".bicep") && path.Base(file) != "bicepconfig.json" {
		return []string{}, nil
	}
	text, err := textOf(b, file)
	if err != nil {
		return nil, err
	}
	refs := []string{}
	if strings.HasSuffix(file, ".bicep") {
		for _, pattern := range []string{`\bmodule\s+\w+\s+'([^']+)'`, `\bfrom\s+'([^']+)'`, `\bextension\s+'([^']+)'`,
			`\bload(Text|Json|Yaml|FileAsBase64)Content\s*\(\s*'([^']+)'`, `\bloadFileAsBase64\s*\(\s*'([^']+)'`} {
			for _, m := range regexp.MustCompile(pattern).FindAllStringSubmatch(text, -1) {
				refs = append(refs, m[len(m)-1])
			}
		}
	}
	if path.Base(file) == "bicepconfig.json" {
		var config struct {
			Extensions map[string]json.RawMessage `json:"extensions"`
		}
		if err := json.Unmarshal(b, &config); err != nil {
			return nil, fmt.Errorf("invalid compiler configuration JSON (contents withheld)")
		}
		keys := []string{}
		for key := range config.Extensions {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			var s string
			if json.Unmarshal(config.Extensions[key], &s) == nil {
				refs = append(refs, s)
			}
		}
	}
	result := []string{}
	for _, ref := range refs {
		if strings.HasPrefix(ref, "br:") || strings.HasPrefix(ref, "ts:") {
			continue
		}
		if strings.Contains(ref, "${") || match(`(?i)^([a-z][a-z0-9+.-]*:|/)`, ref) {
			return nil, fmt.Errorf("non-portable local dependency in %s", file)
		}
		resolved := path.Join(path.Dir(file), ref)
		if err := safePath(resolved); err != nil {
			return nil, err
		}
		result = append(result, resolved)
	}
	return result, nil
}
