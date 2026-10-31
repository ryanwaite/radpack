package radpack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

var ctx = context.Background()

const app = `extension radius
param environment string
resource app 'Radius.Core/applications@2025-08-01-preview' = {
  name: 'fixture'
  properties: { environment: environment }
}
`
const config = `{"extensions":{"radius":"br:example.invalid/radius:0.60.0"}}`
const repo = "https://github.com/radpack-fixture/app.git"

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func fails(t *testing.T, err error, part string) {
	t.Helper()
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(part)) {
		t.Fatalf("want error containing %q, got %v", part, err)
	}
}
func write(t *testing.T, root, file, text string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(file))
	check(t, os.MkdirAll(filepath.Dir(target), 0755))
	check(t, os.WriteFile(target, []byte(text), 0644))
}
func read(t *testing.T, root, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	check(t, err)
	return string(b)
}
func writeJSON(t *testing.T, root, file string, v any) {
	t.Helper()
	b, err := jsonBytes(v)
	check(t, err)
	write(t, root, file, string(b))
}
func g(t *testing.T, dir string, args ...string) string {
	t.Helper()
	s, err := gitText(ctx, dir, args...)
	check(t, err)
	return s
}
func commit(t *testing.T, dir string, files ...string) string {
	t.Helper()
	g(t, dir, append([]string{"add", "--"}, files...)...)
	g(t, dir, "-c", "user.name=Radpack tests", "-c", "user.email=tests@example.invalid", "commit", "--quiet", "-m", "fixture")
	return g(t, dir, "rev-parse", "HEAD")
}

type fixture struct{ root, source, base, pack, destination, gitConfig string }

func newFixture(t *testing.T, upstreamModel bool) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{root: root, source: filepath.Join(root, "source"), pack: filepath.Join(root, "pack"),
		destination: filepath.Join(root, "installed"), gitConfig: filepath.Join(root, "gitconfig")}
	check(t, os.Mkdir(f.source, 0755))
	// Isolate tests from the operator's Git identity, signing, hooks and filters.
	write(t, root, "gitconfig", "")
	t.Setenv("GIT_CONFIG_GLOBAL", f.gitConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS"} {
		t.Setenv(key, "")
	}
	g(t, f.source, "init", "--quiet", "--template=")
	g(t, f.source, "remote", "add", "origin", repo)
	write(t, f.source, "app.js", "upstream\n")
	write(t, f.source, "obsolete.txt", "remove me\n")
	write(t, f.source, ".gitignore", ".env\nignored.txt\n")
	if upstreamModel {
		write(t, f.source, ".radius/app.bicep", app)
		write(t, f.source, ".radius/bicepconfig.json", config)
	}
	f.base = commit(t, f.source, ".")
	if !upstreamModel {
		write(t, f.source, ".radius/app.bicep", app)
		write(t, f.source, ".radius/bicepconfig.json", config)
	}
	// Git config paths with forward slashes work on Windows as well.
	g(t, root, "config", "--global", "url."+filepath.ToSlash(f.source)+".insteadOf", repo)
	g(t, root, "config", "--global", "protocol.file.allow", "always")
	return f
}
func (f *fixture) plan(t *testing.T, include ...string) *Plan {
	t.Helper()
	p, err := PlanCreate(ctx, CreateOptions{Source: f.source, Base: f.base, Include: include})
	check(t, err)
	return p
}
func (f *fixture) publish(t *testing.T, include ...string) *Plan {
	t.Helper()
	p := f.plan(t, include...)
	if len(p.Blockers) > 0 {
		t.Fatal(p.Blockers)
	}
	_, err := WritePack(p, f.pack, p.Approval)
	check(t, err)
	return p
}
func (f *fixture) install(t *testing.T, adapt bool) *ApplicationResult {
	t.Helper()
	r, err := InstallApplication(ctx, f.pack, ApplicationOptions{Directory: f.destination, Latest: adapt, Adapt: adapt}, nil)
	check(t, err)
	return r
}

func TestRoundTrip(t *testing.T) {
	f := newFixture(t, false)
	write(t, f.source, "app.js", "adapted\n")
	write(t, f.source, "start.sh", "#!/bin/sh\nprintf hello\n")
	check(t, os.Chmod(filepath.Join(f.source, "start.sh"), 0755))
	check(t, os.Remove(filepath.Join(f.source, "obsolete.txt")))
	write(t, f.source, ".radius/radius-environment.bicep", "author state")
	write(t, f.source, "notes.txt", "not selected")
	p := f.publish(t, "app.js", "start.sh", "obsolete.txt")
	if len(p.Excluded) != 1 || !reflect.DeepEqual(p.Unselected, []string{"notes.txt"}) {
		t.Fatalf("inventory: %+v", p.Review)
	}
	f.install(t, false)
	if read(t, f.destination, "app.js") != "adapted\n" {
		t.Fatal("missing adaptation")
	}
	if _, err := os.Stat(filepath.Join(f.destination, "obsolete.txt")); !os.IsNotExist(err) {
		t.Fatal("deletion missing")
	}
	r, err := VerifyInstallation(ctx, "", f.destination)
	check(t, err)
	if r.Files != 5 || r.Status != "installed" {
		t.Fatal(r)
	}
	if g(t, f.destination, "rev-parse", "HEAD") != f.base {
		t.Fatal("baseline changed")
	}
}
func TestUpstreamRetained(t *testing.T) {
	f := newFixture(t, true)
	p := f.publish(t)
	if len(p.Payloads) != 0 {
		t.Fatal("upstream assets duplicated")
	}
	for _, file := range p.Manifest.Files {
		if file.Operation != "retain" {
			t.Fatal(file)
		}
	}
	f.install(t, false)
}
func TestLocalDependenciesAndRootModel(t *testing.T) {
	for _, rootModel := range []bool{false, true} {
		t.Run(map[bool]string{true: "root", false: "radius"}[rootModel], func(t *testing.T) {
			f := newFixture(t, false)
			prefix := ".radius/"
			if rootModel {
				check(t, os.RemoveAll(filepath.Join(f.source, ".radius")))
				prefix = ""
			}
			write(t, f.source, prefix+"app.bicep", app+"\nmodule worker './modules/worker.bicep' = { name: 'worker' }\n")
			write(t, f.source, prefix+"bicepconfig.json", config)
			write(t, f.source, prefix+"modules/worker.bicep", "var settings = loadTextContent('./settings.txt')\n")
			write(t, f.source, prefix+"modules/settings.txt", "portable settings")
			p := f.publish(t)
			if len(p.Manifest.Files) != 4 {
				t.Fatal(p.Manifest.Files)
			}
			f.install(t, false)
		})
	}
}
func TestCreateApprovalAndCommittedChanges(t *testing.T) {
	f := newFixture(t, false)
	p := f.plan(t)
	write(t, f.source, ".radius/app.bicep", app+"\n")
	newPlan := f.plan(t)
	if p.Approval == newPlan.Approval {
		t.Fatal("approval did not change")
	}
	_, err := WritePack(newPlan, f.pack, p.Approval)
	fails(t, err, "approval does not match")
	newPlan.Payloads[".radius/app.bicep"] = []byte("altered")
	_, err = WritePack(newPlan, f.pack, newPlan.Approval)
	fails(t, err, "payload changed")
	write(t, f.source, "app.js", "committed adaptation")
	commit(t, f.source, "app.js", ".radius")
	f.publish(t, "app.js")
	f.install(t, false)
	if read(t, f.destination, "app.js") != "committed adaptation" {
		t.Fatal("committed edit omitted")
	}
}
func TestUnselectedAndIgnoredFiles(t *testing.T) {
	f := newFixture(t, false)
	write(t, f.source, "app.js", "not selected")
	write(t, f.source, "\u6587\u6863.md", "unrelated")
	p := f.publish(t)
	if !reflect.DeepEqual(p.Unselected, []string{"app.js"}) || len(p.Excluded) != 1 {
		t.Fatal(p.Review)
	}
	f.install(t, false)
	if read(t, f.destination, "app.js") != "upstream\n" {
		t.Fatal("silently included adaptation")
	}
	write(t, f.destination, "ignored.txt", "leftovers")
	_, err := VerifyInstallation(ctx, f.pack, f.destination)
	fails(t, err, "undeclared")
}
func TestBlockedSelectionAndRedaction(t *testing.T) {
	f := newFixture(t, false)
	write(t, f.source, ".env", "PASSWORD=do-not-export")
	p := f.plan(t, ".env")
	if len(p.Blockers) == 0 {
		t.Fatal("accepted env")
	}
	_, err := WritePack(p, f.pack, p.Approval)
	fails(t, err, "blocked")
	secret := "ghp_" + strings.Repeat("X", 36)
	write(t, f.source, ".radius/app.bicep", app+"\nvar token = '"+secret+"'")
	var out, errOut bytes.Buffer
	if Run(ctx, []string{"create", "--source", f.source, "--base", f.base, "--dry-run"}, &out, &errOut) != 1 {
		t.Fatal("expected blocked CLI")
	}
	if strings.Contains(out.String()+errOut.String(), secret) {
		t.Fatal("secret leaked")
	}
	if !strings.Contains(errOut.String(), "GitHub token") {
		t.Fatal(errOut.String())
	}
}
func TestMissingDependenciesAndLimits(t *testing.T) {
	f := newFixture(t, false)
	write(t, f.source, ".radius/app.bicep", app+"\nmodule x './missing.bicep' = {}")
	if p := f.plan(t); !strings.Contains(strings.Join(p.Blockers, " "), "missing local dependency") {
		t.Fatal(p.Blockers)
	}
	write(t, f.source, ".radius/app.bicep", app)
	write(t, f.source, ".radius/bicepconfig.json", `{"extensions":{"customTypes":"./custom-types.tgz"}}`)
	write(t, f.source, ".radius/custom-types.tgz", "invalid")
	if p := f.plan(t); len(p.Blockers) == 0 {
		t.Fatal("invalid gzip accepted")
	}
	write(t, f.source, ".radius/custom-types.tgz", strings.Repeat("x", maxFileBytes+1))
	if p := f.plan(t); !strings.Contains(strings.Join(p.Blockers, " "), "8 MiB") {
		t.Fatal(p.Blockers)
	}
}
func TestTamperedPack(t *testing.T) {
	for _, kind := range []string{"payload", "extra", "credential-checksum", "precondition", "missing-ref"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, false)
			write(t, f.source, "app.js", "adaptation")
			f.publish(t, "app.js")
			p, err := LoadPack(f.pack)
			check(t, err)
			switch kind {
			case "payload":
				write(t, f.pack, "overlay/.radius/app.bicep", "tampered")
			case "extra":
				write(t, f.pack, "credentials.txt", "unexpected")
			case "credential-checksum":
				text := app + "\nvar config = { password: 'do-not-export' }"
				write(t, f.pack, "overlay/.radius/app.bicep", text)
				for i := range p.Manifest.Files {
					if p.Manifest.Files[i].Path == ".radius/app.bicep" {
						p.Manifest.Files[i].After.SHA256 = hash([]byte(text))
					}
				}
				writeJSON(t, f.pack, "radpack.json", p.Manifest)
			case "precondition":
				for i := range p.Manifest.Files {
					if p.Manifest.Files[i].Path == "app.js" {
						p.Manifest.Files[i].Before.SHA256 = strings.Repeat("0", 64)
					}
				}
				writeJSON(t, f.pack, "radpack.json", p.Manifest)
			case "missing-ref":
				p.Manifest.Upstream.Commit = strings.Repeat("0", 40)
				writeJSON(t, f.pack, "radpack.json", p.Manifest)
			}
			_, err = InstallPack(ctx, f.pack, f.destination, InstallOptions{})
			if err == nil {
				t.Fatal("tampered pack installed")
			}
			if _, err := os.Stat(f.destination); !os.IsNotExist(err) {
				t.Fatal("failed installation left destination")
			}
		})
	}
}
func TestExistingDestinationsUntouched(t *testing.T) {
	f := newFixture(t, false)
	p := f.publish(t)
	_, err := WritePack(p, f.pack, p.Approval)
	fails(t, err, "already exists")
	write(t, f.destination, "mine.txt", "keep")
	_, err = InstallPack(ctx, f.pack, f.destination, InstallOptions{})
	fails(t, err, "already exists")
	if read(t, f.destination, "mine.txt") != "keep" {
		t.Fatal("existing destination changed")
	}
}
func TestVerifyDetectsEdits(t *testing.T) {
	for _, file := range []string{".radius/app.bicep", "app.js", ".radpack/install.json", ".radpack/pack/radpack.json"} {
		t.Run(file, func(t *testing.T) {
			f := newFixture(t, false)
			f.publish(t)
			f.install(t, false)
			write(t, f.destination, file, "{}")
			_, err := VerifyInstallation(ctx, f.pack, f.destination)
			if err == nil {
				t.Fatal("edit undetected")
			}
		})
	}
}
func TestSymlinkRefusal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires Windows privileges")
	}
	f := newFixture(t, false)
	f.publish(t)
	check(t, os.Remove(filepath.Join(f.pack, "overlay/.radius/app.bicep")))
	check(t, os.Symlink(filepath.Join(f.source, ".radius/app.bicep"), filepath.Join(f.pack, "overlay/.radius/app.bicep")))
	_, err := LoadPack(f.pack)
	fails(t, err, "symbolic")
	check(t, os.RemoveAll(filepath.Join(f.source, ".radius")))
	check(t, os.Symlink(f.pack, filepath.Join(f.source, ".radius")))
	_, err = PlanCreate(ctx, CreateOptions{Source: f.source, Base: f.base})
	fails(t, err, "symbolic")
}
func TestUndeclaredDependency(t *testing.T) {
	f := newFixture(t, false)
	write(t, f.source, ".radius/app.bicep", app+"\nvar data = loadTextContent('../app.js')\n")
	f.publish(t)
	p, err := LoadPack(f.pack)
	check(t, err)
	files := []FileOperation{}
	for _, file := range p.Manifest.Files {
		if file.Path != "app.js" {
			files = append(files, file)
		}
	}
	p.Manifest.Files = files
	writeJSON(t, f.pack, "radpack.json", p.Manifest)
	_, err = InstallPack(ctx, f.pack, f.destination, InstallOptions{})
	fails(t, err, "undeclared local dependency")
}
func TestGitEnvironmentIsolation(t *testing.T) {
	f := newFixture(t, false)
	t.Setenv("GIT_DIR", filepath.Join(f.root, "wrong"))
	p := f.plan(t)
	if p.Manifest.Upstream.Commit != f.base {
		t.Fatal("redirected Git")
	}
}
func TestManifestStrictness(t *testing.T) {
	f := newFixture(t, false)
	p := f.plan(t)
	original, err := jsonBytes(p.Manifest)
	check(t, err)
	for _, mutation := range []func(map[string]any){
		func(v map[string]any) { v["hooks"] = map[string]string{"install": "execute-me"} },
		func(v map[string]any) { v["formatVersion"] = 999 },
		func(v map[string]any) { v["name"] = 123 },
		func(v map[string]any) { v["Name"] = "case-alias" },
		func(v map[string]any) { delete(v, "radiusVersion") },
		func(v map[string]any) { v["files"].([]any)[0].(map[string]any)["path"] = "../outside" },
		func(v map[string]any) { v["files"].([]any)[0].(map[string]any)["operation"] = "shell" },
		func(v map[string]any) {
			v["files"].([]any)[0].(map[string]any)["after"].(map[string]any)["mode"] = "120000"
		},
		func(v map[string]any) { v["files"] = append(v["files"].([]any), v["files"].([]any)[0]) },
		func(v map[string]any) {
			v["upstream"].(map[string]any)["repository"] = "https://token@github.com/owner/repo"
		},
	} {
		var value map[string]any
		check(t, json.Unmarshal(original, &value))
		mutation(value)
		data, err := json.Marshal(value)
		check(t, err)
		var m Manifest
		err = decodeStrict(data, &m)
		if err == nil {
			err = validateManifest(m)
		}
		if err == nil {
			t.Fatal("invalid manifest accepted")
		}
	}
	var m Manifest
	err = decodeStrict(bytes.Replace(original, []byte(`"formatVersion": 1`), []byte(`"formatVersion": 1, "formatVersion": 1`), 1), &m)
	fails(t, err, "invalid JSON")
	p.Manifest.Files = append(p.Manifest.Files, FileOperation{Path: ".radius", Operation: "add", After: p.Manifest.Files[0].After})
	fails(t, validateManifest(p.Manifest), "overlap")
}
func TestPathsAndRepositoryURLs(t *testing.T) {
	for _, file := range []string{"../x", "/x", "a/../../x", ".git/config", "a\\.git\\config", "CON", "a/NUL.txt", "x:y", "a//b", "a/./b", "a/b.", "a/\nsecret", ".git~1/config", ".git\u200c/hooks"} {
		if safePath(file) == nil {
			t.Fatalf("accepted unsafe path %q", file)
		}
	}
	for _, file := range []string{".radius/radius-environment.bicep", ".radius/radius-recipe-pack.bicep", ".radius-target-kubeconfig", ".radius-credentials.json",
		".rad/config.yaml", ".radius/.staging-test/app.bicep", ".aws/credentials", "terraform.tfstate", "prod.bicepparam", ".github/workflows/deploy.yml", ".env", ".radpack/install.json", ".npmrc", "secrets.json", ".docker/config.json"} {
		reason, err := excludedReason(file)
		check(t, err)
		if reason == "" {
			t.Fatal("not excluded", file)
		}
	}
	for _, input := range []string{"owner/app", "git@github.com:owner/app.git", "https://github.com/owner/app.git"} {
		s, err := repositoryURL(input)
		check(t, err)
		if s != "https://github.com/owner/app.git" {
			t.Fatal(s)
		}
	}
	for _, input := range []string{"file:///tmp/repo", "ext::command", "https://user:secret@github.com/owner/app", "https://github.com/owner/app?token=x", "https://example.com/owner/app"} {
		if _, err := repositoryURL(input); err == nil {
			t.Fatal("accepted URL", input)
		}
	}
}
func TestContentPolicy(t *testing.T) {
	for _, c := range []struct{ file, text string }{
		{"app.js", "const config = { password: 'do-not-export' }"},
		{"settings.json", `{"apiKey":"do-not-export"}`}, {"settings.json", `{"token":"do-not-export"}`},
		{"settings.json", `{"subscriptionId":"author-subscription"}`},
		{"settings.yml", "AWS_SECRET_ACCESS_KEY: do-not-export"}, {"settings.txt", "-----BEGIN PRIVATE KEY-----"},
		{"settings.txt", "postgres://person:password@host/db"},
		{"app.bicep", "@secure()\nparam password string = 'bad-default'"},
		{"app.bicep", app + "\nresource creds 'Radius.Security/secrets@2025-08-01-preview' = { properties: { data: { password: { value: 'bad' } } } }"},
		{"secret.yaml", "kind: Secret\nstringData:\n  anything: value\n"},
		{"app.bicep", "param environment string = 'author-env'"},
		{"app.bicep", "resource env 'Radius.Core/environments@2025-08-01-preview' = {}"},
	} {
		t.Run(c.file+"/"+c.text[:min(12, len(c.text))], func(t *testing.T) {
			findings, err := inspectContent([]byte(c.text), c.file)
			check(t, err)
			if len(findings) == 0 {
				t.Fatal("sensitive content accepted")
			}
		})
	}
	portable := app + `
@secure()
param brokerPassword string
param queue string = 'orders'
resource creds 'Radius.Security/secrets@2025-08-01-preview' = {
 properties: { data: { password: { value: brokerPassword } } }
}
resource worker 'Radius.Compute/containers@2025-08-01-preview' = {
 properties: { env: { QUEUE: { value: 'orders' } } }
}
`
	check(t, assertPortable([]byte(portable), "app.bicep"))
	check(t, assertPortable([]byte("PASSWORD=<provide-at-deployment>"), ".env.example"))
	if assertPortable([]byte{0, 255}, "binary.dat") == nil {
		t.Fatal("binary accepted")
	}
}
func TestDependencyDiscovery(t *testing.T) {
	deps, err := dependencies([]byte("import { setting } from './shared.bicep'\nvar cfg = loadJsonContent('./settings.json')"), ".radius/app.bicep")
	check(t, err)
	if !reflect.DeepEqual(deps, []string{".radius/shared.bicep", ".radius/settings.json"}) {
		t.Fatal(deps)
	}
	for _, ref := range []string{"../../outside.bicep", "/outside.bicep", "https://example.invalid/file"} {
		_, err := dependencies([]byte("module x '"+ref+"' = {}"), ".radius/app.bicep")
		if err == nil {
			t.Fatal("unsafe dependency")
		}
	}
}
func archive(t *testing.T, name string, data []byte, kind byte) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	check(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(data)), Typeflag: kind}))
	if len(data) > 0 {
		_, err := tw.Write(data)
		check(t, err)
	}
	check(t, tw.Close())
	check(t, gz.Close())
	return b.Bytes()
}
func TestCustomTypeArchives(t *testing.T) {
	f := newFixture(t, false)
	b := archive(t, "types.json", []byte(`{"types":[]}`), tar.TypeReg)
	write(t, f.source, ".radius/custom-types.tgz", string(b))
	write(t, f.source, ".radius/bicepconfig.json", `{"extensions":{"customTypes":"./custom-types.tgz"}}`)
	f.publish(t)
	f.install(t, false)
	if read(t, f.destination, ".radius/custom-types.tgz") != string(b) {
		t.Fatal("archive bytes changed")
	}
	for _, c := range []struct {
		name string
		data []byte
		kind byte
	}{
		{"../outside.json", []byte("{}"), tar.TypeReg}, {"/outside.json", []byte("{}"), tar.TypeReg},
		{".git/config", []byte("{}"), tar.TypeReg}, {"types.json", nil, tar.TypeSymlink},
		{"types.json", []byte{0, 255}, tar.TypeReg}, {"data.json", []byte(`{"password":"do-not-export"}`), tar.TypeReg},
		{".radius-credentials.json", []byte("{}"), tar.TypeReg},
	} {
		if assertPortableFile(archive(t, c.name, c.data, c.kind), ".radius/custom-types.tgz") == nil {
			t.Fatal("unsafe archive accepted", c.name)
		}
	}
	var oversized bytes.Buffer
	gz := gzip.NewWriter(&oversized)
	_, err := gz.Write(make([]byte, maxPackBytes+1))
	check(t, err)
	check(t, gz.Close())
	if assertPortableFile(oversized.Bytes(), ".radius/custom-types.tgz") == nil {
		t.Fatal("oversized gzip accepted")
	}
}

func descriptor() assetManifest {
	return assetManifest{1, "in-repository", "fixture", ".radius/app.bicep", nil, []string{".radius/app.bicep", ".radius/bicepconfig.json"}}
}
func TestInRepositoryDiscovery(t *testing.T) {
	for _, location := range []string{".radpack", "integrations/radius"} {
		t.Run(location, func(t *testing.T) {
			f := newFixture(t, false)
			writeJSON(t, f.source, location+"/radpack.json", descriptor())
			head := commit(t, f.source, ".radius", location)
			o := ApplicationOptions{Directory: f.destination, Catalog: []byte("bad catalog")}
			if location != ".radpack" {
				o.PackPath = location
			}
			r, err := InstallApplication(ctx, "radpack-fixture/app", o, nil)
			check(t, err)
			if r.Status != "installed" || r.SelectedCommit != head || r.PackSource.Kind != "in-repository" {
				t.Fatal(r)
			}
			p, err := LoadPack(filepath.Join(f.destination, ".radpack/pack"))
			check(t, err)
			for _, file := range p.Manifest.Files {
				if file.Operation != "retain" {
					t.Fatal("duplicated in-repo asset")
				}
			}
			_, err = VerifyInstallation(ctx, "", f.destination)
			check(t, err)
		})
	}
}
func publisher(t *testing.T, f *fixture) (string, string) {
	t.Helper()
	dir := filepath.Join(f.root, "publisher")
	check(t, os.Mkdir(dir, 0755))
	g(t, dir, "init", "--quiet", "--template=")
	check(t, filepath.WalkDir(f.pack, func(file string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(f.pack, file)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		write(t, dir, "contrib/app/"+filepath.ToSlash(rel), string(b))
		return nil
	}))
	sha := commit(t, dir, ".")
	g(t, f.root, "config", "--global", "url."+filepath.ToSlash(dir)+".insteadOf", "https://github.com/radpack-fixture/packs.git")
	return dir, "https://github.com/radpack-fixture/packs/tree/" + sha + "/contrib/app"
}
func TestCatalogAndGitHubOverride(t *testing.T) {
	for _, mode := range []string{"catalog", "override"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, false)
			f.publish(t)
			dir, location := publisher(t, f)
			write(t, f.source, "app.js", "new upstream")
			head := commit(t, f.source, "app.js")
			o := ApplicationOptions{Directory: f.destination}
			if mode == "catalog" {
				b, err := jsonBytes(catalog{1, map[string]string{"radpack-fixture/app": location}})
				check(t, err)
				o.Catalog = b
			} else {
				g(t, dir, "branch", "published")
				o.Pack = strings.Replace(location, g(t, dir, "rev-parse", "HEAD"), "published", 1)
			}
			r, err := InstallApplication(ctx, "radpack-fixture/app", o, nil)
			check(t, err)
			if r.SelectedCommit == head || r.SelectedCommit != f.base || r.PackSource.Commit != g(t, dir, "rev-parse", "HEAD") {
				t.Fatal(r)
			}
			_, err = VerifyInstallation(ctx, "", f.destination)
			check(t, err)
		})
	}
}
func TestDiscoveryErrorsAndPrecedence(t *testing.T) {
	f := newFixture(t, false)
	f.publish(t)
	_, err := InstallApplication(ctx, "radpack-fixture/app", ApplicationOptions{Directory: f.destination}, nil)
	fails(t, err, "no Radpack found")
	_, err = InstallApplication(ctx, "unrelated/app", ApplicationOptions{Directory: f.destination, Pack: f.pack}, nil)
	fails(t, err, "different application")
	_, err = InstallApplication(ctx, "radpack-fixture/app", ApplicationOptions{PackPath: "missing", Directory: f.destination}, nil)
	fails(t, err, "fallback is disabled")
	b, err := jsonBytes(catalog{1, map[string]string{"radpack-fixture/app": "https://github.com/owner/packs/tree/main/app"}})
	check(t, err)
	_, err = InstallApplication(ctx, "radpack-fixture/app", ApplicationOptions{Directory: f.destination, Catalog: b}, nil)
	fails(t, err, "must pin")
	write(t, f.source, ".radpack/radpack.json", "{broken")
	commit(t, f.source, ".radpack")
	_, err = InstallApplication(ctx, "radpack-fixture/app", ApplicationOptions{Directory: f.destination}, nil)
	fails(t, err, "invalid JSON")
	r, err := InstallApplication(ctx, "radpack-fixture/app", ApplicationOptions{Directory: f.destination, Pack: f.pack}, nil)
	check(t, err)
	if r.SelectedCommit != f.base {
		t.Fatal("explicit override lost precedence")
	}
}
func TestUnsafeDescriptor(t *testing.T) {
	for _, extra := range []string{"../outside", ".env"} {
		t.Run(extra, func(t *testing.T) {
			f := newFixture(t, false)
			d := descriptor()
			d.Assets = append(d.Assets, extra)
			writeJSON(t, f.source, ".radpack/radpack.json", d)
			commit(t, f.source, ".radius", ".radpack")
			_, err := InstallApplication(ctx, "radpack-fixture/app", ApplicationOptions{Directory: f.destination}, nil)
			if err == nil {
				t.Fatal("unsafe descriptor installed")
			}
		})
	}
}
func TestLatestConflicts(t *testing.T) {
	for _, kind := range []string{"conflict", "deleted", "already-applied", "unrelated"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, false)
			write(t, f.source, "app.js", "pack adaptation")
			f.publish(t, "app.js")
			switch kind {
			case "conflict":
				write(t, f.source, "app.js", "new upstream")
				write(t, f.source, ".radius/app.bicep", app+"\n// new model")
				commit(t, f.source, "app.js", ".radius")
			case "deleted":
				check(t, os.Remove(filepath.Join(f.source, "app.js")))
				commit(t, f.source, "app.js")
			case "already-applied":
				commit(t, f.source, "app.js")
			case "unrelated":
				write(t, f.source, "app.js", "upstream\n")
				write(t, f.source, "NEWS.md", "unrelated")
				commit(t, f.source, "NEWS.md")
			}
			r := f.install(t, true)
			if r.Status != "adaptation-required" {
				t.Fatal(r)
			}
			var request Adaptation
			check(t, decodeStrict([]byte(read(t, f.destination, ".radpack/adaptation.json")), &request))
			if request.DeploymentStatus != "unverified" || request.ReferenceCommit != f.base {
				t.Fatal(request)
			}
			if read(t, f.destination, ".radpack/reference/app.js") != "pack adaptation" {
				t.Fatal("reference lost")
			}
			switch kind {
			case "conflict":
				if len(request.Conflicts) != 2 || read(t, f.destination, "app.js") != "new upstream" {
					t.Fatal(request)
				}
			case "deleted":
				if len(request.Conflicts) != 1 {
					t.Fatal(request)
				}
				if _, err := os.Stat(filepath.Join(f.destination, "app.js")); !os.IsNotExist(err) {
					t.Fatal("deleted file restored")
				}
			default:
				if len(request.Conflicts) != 0 || read(t, f.destination, "app.js") != "pack adaptation" {
					t.Fatal(request)
				}
			}
			v, err := VerifyInstallation(ctx, "", f.destination)
			check(t, err)
			if v.Status != "adaptation-required" {
				t.Fatal(v)
			}
			if !strings.Contains(read(t, f.destination, ".radpack/ADAPT.md"), "assembled application") {
				t.Fatal("build-source warning lost")
			}
			write(t, f.destination, ".radpack/adaptation.json", "{}")
			_, err = VerifyInstallation(ctx, "", f.destination)
			fails(t, err, "metadata changed")
		})
	}
}
func TestLatestSafeDeleteAndPolicy(t *testing.T) {
	f := newFixture(t, false)
	check(t, os.Remove(filepath.Join(f.source, "obsolete.txt")))
	f.publish(t, "obsolete.txt")
	write(t, f.source, "obsolete.txt", "remove me\n")
	write(t, f.source, "NEWS.md", "new")
	commit(t, f.source, "NEWS.md")
	f.install(t, true)
	if _, err := os.Stat(filepath.Join(f.destination, "obsolete.txt")); !os.IsNotExist(err) {
		t.Fatal("safe deletion not applied")
	}
	p, err := PlanCreate(ctx, CreateOptions{Source: f.destination, Base: g(t, f.destination, "rev-parse", "HEAD")})
	check(t, err)
	for _, file := range p.Manifest.Files {
		if strings.HasPrefix(file.Path, ".radpack/") {
			t.Fatal("metadata exported")
		}
	}
	write(t, f.source, ".radius/radius-environment.bicep", "author")
	commit(t, f.source, ".radius/radius-environment.bicep")
	_, err = InstallApplication(ctx, f.pack, ApplicationOptions{Directory: filepath.Join(f.root, "bad"), Latest: true, Adapt: true}, nil)
	fails(t, err, "non-portable Radius environment material")
}
func TestRevisionAndURLValidation(t *testing.T) {
	f := newFixture(t, false)
	g(t, f.source, "branch", "ambiguous")
	g(t, f.source, "tag", "ambiguous")
	_, err := resolveRevision(ctx, repo, "ambiguous")
	fails(t, err, "both a branch and a tag")
	g(t, f.source, "-c", "user.name=Radpack tests", "-c", "user.email=tests@example.invalid", "tag", "-a", "release", "-m", "release")
	s, err := resolveRevision(ctx, repo, "release")
	check(t, err)
	if s != f.base {
		t.Fatal(s)
	}
	for _, value := range []string{"https://user:token@github.com/owner/packs/tree/main/app", "https://github.com/owner/packs/tree/main/app?token=x",
		"http://github.com/owner/packs/tree/main/app", "https://example.com/owner/packs/tree/main/app",
		"https://github.com/owner/packs/tree/main/%2e%2e%2foutside", "https://github.com/owner/packs/tree/main/.git/hooks",
		"https://github.com/owner/packs/tree/feature%2fbranch/app"} {
		if _, err := parsePackURL(value); err == nil {
			t.Fatal("unsafe URL accepted")
		}
	}
	for _, o := range []ApplicationOptions{{Latest: true}, {Adapt: true}, {Pack: "x", PackPath: "y"}} {
		if _, err := InstallApplication(ctx, "owner/repo", o, nil); err == nil {
			t.Fatal("bad options accepted")
		}
	}
}
func TestLegacyReceipt(t *testing.T) {
	f := newFixture(t, false)
	f.publish(t)
	f.install(t, false)
	var r Receipt
	check(t, decodeStrict([]byte(read(t, f.destination, receiptPath)), &r))
	writeJSON(t, f.destination, receiptPath, LegacyReceipt{1, r.PackManifestSHA256, r.Manifest})
	check(t, os.RemoveAll(filepath.Join(f.destination, ".radpack/pack")))
	_, err := VerifyInstallation(ctx, f.pack, f.destination)
	check(t, err)
}
func TestCLI(t *testing.T) {
	f := newFixture(t, false)
	invoke := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := 0
		if err := run(ctx, args, &out, func(context.Context) (string, error) { return "0.60.0", nil }); err != nil {
			code = 1
			errOut.WriteString(err.Error())
		}
		return code, out.String(), errOut.String()
	}
	code, out, errs := invoke("create", "--source", f.source, "--base", f.base, "--output", f.pack, "--dry-run")
	if code != 0 {
		t.Fatal(errs)
	}
	const marker = "Review SHA: "
	start := strings.Index(out, marker)
	if start < 0 {
		t.Fatal(out)
	}
	approval := strings.Fields(out[start+len(marker):])[0]
	code, _, errs = invoke("create", "--source", f.source, "--base="+f.base, "--output", f.pack, "--approve", approval)
	if code != 0 {
		t.Fatal(errs)
	}
	code, out, errs = invoke("inspect", f.pack)
	if code != 0 || !strings.Contains(out, "manifestSha256") {
		t.Fatal(out, errs)
	}
	code, out, errs = invoke("install", "radpack-fixture/app", "--pack", f.pack, "--directory", f.destination, "--latest", "--adapt")
	if code != 0 || !strings.Contains(out, "adaptation-required") {
		t.Fatal(out, errs)
	}
	code, out, errs = invoke("verify", "--directory", f.destination)
	if code != 0 || !strings.Contains(out, "Status: adaptation-required") {
		t.Fatal(out, errs)
	}
	for _, args := range [][]string{{"create", "--password", "do-not-print"}, {"create", "--approve", "x", "--dry-run"}, {"run"}, {"verify", f.pack}} {
		code, out, errs = invoke(args...)
		if code != 1 || strings.Contains(out+errs, "do-not-print") {
			t.Fatal(code, out, errs)
		}
	}
	code, out, errs = invoke("--help")
	if code != 0 || !strings.Contains(out, "cloud of choice") {
		t.Fatal(out, errs)
	}
}
func TestNativeExecutable(t *testing.T) {
	f := newFixture(t, false)
	f.publish(t)
	executable := filepath.Join(f.root, "radpack")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}

	build := exec.Command("go", "build", "-buildvcs=false", "-o", executable, "../../cmd/radpack")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", output, err)
	}
	command := exec.Command(executable, "install", "radpack-fixture/app", "--pack", f.pack)
	command.Dir = f.root
	// Use a harmless executable that rejects the version query, never the
	// developer's real Radius installation.
	command.Env = append(os.Environ(), "RADIUS_RAD_BINARY="+executable)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native install: %s: %v", output, err)
	}
	_, err := VerifyInstallation(ctx, "", filepath.Join(f.root, "app"))
	check(t, err)
}

func TestGitOutputBound(t *testing.T) {
	var out limitedBuffer
	_, err := io.Copy(&out, strings.NewReader(strings.Repeat("x", maxPackBytes+1)))
	fails(t, err, "exceeds limit")
	if out.buffer.Len() > maxPackBytes {
		t.Fatal("buffer limit bypassed")
	}
}

func TestMalformedAndDuplicateJSON(t *testing.T) {
	for _, text := range []string{
		`{"formatVersion":1,"applications":{}} trailing`,
		`{"formatVersion":1,"applications":{"owner/app":"x","owner/app":"y"}}`,
		`{"formatVersion":1,"applications":null}`,
		`{"formatVersion":null,"applications":{}}`,
		`{"formatVersion":1,"applications":{},"Applications":{}}`,
		`{"formatVersion":1,"applications":[]}`,
	} {
		var c catalog
		if decodeStrict([]byte(text), &c) == nil {
			t.Fatal("invalid catalog accepted")
		}
	}
}

func TestCanceledGit(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := git(cancelled, "", "--version")
	fails(t, err, "Git --version failed")
}
