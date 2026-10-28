package radpack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
)

const (
	maxFileBytes = 8 * 1024 * 1024
	maxPackBytes = 32 * 1024 * 1024
	receiptPath  = ".radpack/install.json"
)

var (
	commitPattern  = regexp.MustCompile(`^[a-f0-9]{40}$`)
	hashPattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
	namePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	versionPattern = regexp.MustCompile(`^\d+\.\d+(\.\d+)?(-[a-zA-Z0-9.-]+)?$`)
)

type Fingerprint struct {
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode"`
}
type Upstream struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
}
type FileOperation struct {
	Path      string       `json:"path"`
	Operation string       `json:"operation"`
	Before    *Fingerprint `json:"before"`
	After     *Fingerprint `json:"after"`
}
type Manifest struct {
	FormatVersion int             `json:"formatVersion"`
	Name          string          `json:"name"`
	Upstream      Upstream        `json:"upstream"`
	Application   string          `json:"application"`
	RadiusVersion *string         `json:"radiusVersion"`
	Files         []FileOperation `json:"files"`
}
type Excluded struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}
type Review struct {
	Manifest   Manifest   `json:"manifest"`
	Excluded   []Excluded `json:"excluded"`
	Unselected []string   `json:"unselected"`
	Blockers   []string   `json:"blockers"`
}
type Plan struct {
	Review
	Approval string
	Payloads map[string][]byte
}
type Pack struct {
	Manifest Manifest
	Payloads map[string][]byte
	Digest   string
	Raw      []byte
}
type PackSource struct {
	Kind           string `json:"kind"`
	Repository     string `json:"repository,omitempty"`
	Commit         string `json:"commit,omitempty"`
	Path           string `json:"path,omitempty"`
	ManifestSHA256 string `json:"manifestSha256"`
}
type InstalledFile struct {
	Path  string       `json:"path"`
	After *Fingerprint `json:"after"`
}
type Metadata struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type Receipt struct {
	FormatVersion      int             `json:"formatVersion"`
	PackManifestSHA256 string          `json:"packManifestSha256"`
	Manifest           Manifest        `json:"manifest"`
	ApplicationCommit  string          `json:"applicationCommit"`
	PackSource         PackSource      `json:"packSource"`
	Status             string          `json:"status"`
	Files              []InstalledFile `json:"files"`
	Metadata           []Metadata      `json:"metadata"`
}
type LegacyReceipt struct {
	FormatVersion      int      `json:"formatVersion"`
	PackManifestSHA256 string   `json:"packManifestSha256"`
	Manifest           Manifest `json:"manifest"`
}
type Adaptation struct {
	FormatVersion    int        `json:"formatVersion"`
	Status           string     `json:"status"`
	Repository       string     `json:"repository"`
	ReferenceCommit  string     `json:"referenceCommit"`
	SelectedCommit   string     `json:"selectedCommit"`
	PackSource       PackSource `json:"packSource"`
	Conflicts        []string   `json:"conflicts"`
	Application      string     `json:"application"`
	ReferenceAssets  []string   `json:"referenceAssets"`
	AppliedPaths     []string   `json:"appliedPaths"`
	DeploymentStatus string     `json:"deploymentStatus"`
}

func hash(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func jsonBytes(value any) ([]byte, error) {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	err := encoder.Encode(value)
	return b.Bytes(), err
}
func same(a, b *Fingerprint) bool { return reflect.DeepEqual(a, b) }

// Reject duplicate keys as well as unknown, missing, and null required fields.
// Diagnostics deliberately never echo untrusted JSON values.
func decodeStrict(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueJSON(d); err != nil {
		return fmt.Errorf("invalid JSON; contents withheld")
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("invalid JSON; contents withheld")
	}
	if err := requiredJSON(data, reflect.TypeOf(target).Elem()); err != nil {
		return err
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON fields or types; contents withheld")
	}
	return nil
}
func uniqueJSON(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		keys := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || keys[s] {
				return fmt.Errorf("duplicate key")
			}
			keys[s] = true
			if err := uniqueJSON(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueJSON(d); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("invalid delimiter")
	}
	_, err = d.Token()
	return err
}
func requiredJSON(data []byte, t reflect.Type) error {
	if t.Kind() == reflect.Pointer {
		if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
			return nil
		}
		return requiredJSON(data, t.Elem())
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("required JSON field is null")
	}
	switch t.Kind() {
	case reflect.Struct:
		var obj map[string]json.RawMessage
		if json.Unmarshal(data, &obj) != nil {
			return fmt.Errorf("invalid JSON object")
		}
		allowed := map[string]bool{}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			tag := field.Tag.Get("json")
			name := tag
			optional := false
			for j, c := range tag {
				if c == ',' {
					name = tag[:j]
					optional = true
					break
				}
			}
			allowed[name] = true
			raw, ok := obj[name]
			if !ok {
				if optional {
					continue
				}
				return fmt.Errorf("missing required JSON field %s", name)
			}
			if err := requiredJSON(raw, field.Type); err != nil {
				return err
			}
		}
		// encoding/json accepts case-insensitive field aliases. The wire contract
		// does not: an extra "Name" must not override a validated "name".
		for key := range obj {
			if !allowed[key] {
				return fmt.Errorf("unexpected JSON field (contents withheld)")
			}
		}
	case reflect.Slice:
		var items []json.RawMessage
		if json.Unmarshal(data, &items) != nil {
			return fmt.Errorf("invalid JSON array")
		}
		for _, raw := range items {
			if err := requiredJSON(raw, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateFingerprint(f *Fingerprint) error {
	if f != nil && (!hashPattern.MatchString(f.SHA256) || (f.Mode != "100644" && f.Mode != "100755")) {
		return fmt.Errorf("invalid file checksum or mode")
	}
	return nil
}
