package radpack

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func reviewCreate(t *testing.T, f *fixture, answer string, flags ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	ui := &createUI{bufio.NewReader(strings.NewReader(answer)), &out}
	args := append([]string{"create", "--source", f.source, "--output", f.pack}, flags...)
	err := runWithUI(ctx, args, &out, nil, ui)
	return out.String(), err
}

func notCreated(t *testing.T, destination string) {
	t.Helper()
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("unexpected output exists or cannot be inspected: %v", err)
	}
}

func TestCheckoutFirstPublishedPreparation(t *testing.T) {
	f := newFixture(t, true)
	before := g(t, f.source, "status", "--porcelain")
	out, err := reviewCreate(t, f, "y\n")
	check(t, err)
	if !strings.Contains(out, "Create this pack? [y/N]") || !strings.Contains(out, "retain upstream") ||
		!strings.Contains(out, "verified published") {
		t.Fatal(out)
	}
	p, err := LoadPack(f.pack)
	check(t, err)
	if p.Manifest.Upstream.Commit != f.base || len(p.Payloads) != 0 {
		t.Fatal(p.Manifest)
	}
	if g(t, f.source, "status", "--porcelain") != before {
		t.Fatal("author checkout changed")
	}
	f.install(t, false)
}

func TestInteractiveIndividualFileSelection(t *testing.T) {
	for _, selectFile := range []bool{false, true} {
		t.Run(map[bool]string{false: "exclude-by-default", true: "include-one"}[selectFile], func(t *testing.T) {
			f := newFixture(t, false)
			write(t, f.source, "app.js", "adapted\n")
			write(t, f.source, ".env", "password=not-for-export")
			answer := "\ny\n"
			if selectFile {
				answer = "1\ny\n"
			}
			out, err := reviewCreate(t, f, answer)
			check(t, err)
			if !strings.Contains(out, "Include individual application changes?") {
				t.Fatal(out)
			}
			p, err := LoadPack(f.pack)
			check(t, err)
			found := false
			for _, file := range p.Manifest.Files {
				if file.Path == "app.js" {
					found = true
				}
				if file.Path == ".env" {
					t.Fatal("environment selected")
				}
			}
			if found != selectFile {
				t.Fatal("wrong selection", p.Manifest)
			}
			f.install(t, false)
			want := "upstream\n"
			if selectFile {
				want = "adapted\n"
			}
			if read(t, f.destination, "app.js") != want {
				t.Fatal("wrong installed application")
			}
		})
	}
}

func TestInteractiveCancelAndEOF(t *testing.T) {
	for _, answer := range []string{"", "\n", "n\n", "no\n", "yes"} {
		t.Run("answer-"+answer, func(t *testing.T) {
			f := newFixture(t, true)
			out, err := reviewCreate(t, f, answer)
			check(t, err)
			if !strings.Contains(out, "Cancelled") {
				t.Fatal(out)
			}
			notCreated(t, f.pack)
		})
	}
	f := newFixture(t, false)
	write(t, f.source, "app.js", "adaptation")
	_, err := reviewCreate(t, f, "1,1\ny\n")
	fails(t, err, "distinct file numbers")
	notCreated(t, f.pack)
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("automation must not read stdin") }

func TestNoninteractivePreviewAndExplicitApproval(t *testing.T) {
	f := newFixture(t, false)
	var preview strings.Builder
	ui := &createUI{bufio.NewReader(panicReader{}), &preview}
	args := []string{"create", "--source", f.source, "--output", f.pack}
	err := runWithUI(ctx, append(args, "--dry-run"), &preview, nil, ui)
	check(t, err)
	if strings.Contains(preview.String(), "Create this pack?") {
		t.Fatal("dry run prompted")
	}
	notCreated(t, f.pack)
	approval := strings.Fields(strings.Split(preview.String(), "Review SHA: ")[1])[0]
	var saved strings.Builder
	check(t, runWithUI(ctx, append(args, "--approve", approval), &saved, nil, ui))
	_, err = LoadPack(f.pack)
	check(t, err)

	f2 := newFixture(t, true)
	var nonterminal strings.Builder
	check(t, run(ctx, []string{"create", "--source", f2.source, "--output", f2.pack}, &nonterminal, nil))
	if !strings.Contains(nonterminal.String(), "No files written") || strings.Contains(nonterminal.String(), "Create this pack?") {
		t.Fatal(nonterminal.String())
	}
	notCreated(t, f2.pack)
}

type mutatingReader struct {
	reader io.Reader
	mutate func()
}

func (r *mutatingReader) Read(p []byte) (int, error) {
	if r.mutate != nil {
		r.mutate()
		r.mutate = nil
	}
	return r.reader.Read(p)
}

func TestInteractiveRejectsChangedContent(t *testing.T) {
	f := newFixture(t, true)
	var out strings.Builder
	reader := &mutatingReader{strings.NewReader("y\n"), func() {
		write(t, f.source, ".radius/app.bicep", app+"\n// changed while reviewing\n")
	}}
	err := runWithUI(ctx, []string{"create", "--source", f.source, "--output", f.pack},
		&out, nil, &createUI{bufio.NewReader(reader), &out})
	fails(t, err, "changed during review")
	notCreated(t, f.pack)
}

func TestStaleAutomationApprovalAndNewDependencies(t *testing.T) {
	f := newFixture(t, false)
	var out strings.Builder
	args := []string{"create", "--source", f.source, "--output", f.pack}
	check(t, run(ctx, append(args, "--dry-run"), &out, nil))
	approval := strings.Fields(strings.Split(out.String(), "Review SHA: ")[1])[0]
	write(t, f.source, ".radius/app.bicep", app+"\nvar setting = loadTextContent('./settings.txt')")
	write(t, f.source, ".radius/settings.txt", "new dependency")
	err := run(ctx, append(args, "--approve", approval), io.Discard, nil)
	fails(t, err, "approval does not match")
	notCreated(t, f.pack)
}

// Separate the published repository from the author's object database so local
// commits cannot accidentally pass the published-revision check.
func publishedClone(t *testing.T, f *fixture) string {
	t.Helper()
	remote := filepath.Join(f.root, "published")
	g(t, f.root, "clone", "--quiet", "--no-hardlinks", f.source, remote)
	g(t, f.root, "config", "--global", "--unset-all", "url."+filepath.ToSlash(f.source)+".insteadOf")
	g(t, f.root, "config", "--global", "url."+filepath.ToSlash(remote)+".insteadOf", repo)
	return remote
}

func TestLocalOnlyCommitRequiresBaselineChoice(t *testing.T) {
	for _, answer := range []string{"", "\n", "y\n1\ny\n"} {
		t.Run(answer, func(t *testing.T) {
			f := newFixture(t, false)
			remote := publishedClone(t, f)
			g(t, remote, "checkout", "--quiet", "-b", "release-track")
			write(t, remote, "app.js", "new upstream\n")
			published := commit(t, remote, "app.js")
			write(t, f.source, "app.js", "local-only\n")
			commit(t, f.source, "app.js", ".radius")
			var nonterminal strings.Builder
			err := run(ctx, []string{"create", "--source", f.source, "--output", f.pack, "--dry-run"}, &nonterminal, nil)
			fails(t, err, "not confirmed published")
			if !strings.Contains(err.Error(), "release-track") || !strings.Contains(err.Error(), published) {
				t.Fatal(err)
			}
			out, err := reviewCreate(t, f, answer)
			check(t, err)
			if answer == "" || answer == "\n" {
				notCreated(t, f.pack)
				return
			}
			if !strings.Contains(out, "release-track") || !strings.Contains(out, "replace") {
				t.Fatal(out)
			}
			p, err := LoadPack(f.pack)
			check(t, err)
			if p.Manifest.Upstream.Commit != published {
				t.Fatal(p.Manifest)
			}
			// This baseline did not exist in the author's object database.
			if read(t, f.source, "app.js") != "local-only\n" {
				t.Fatal("author checkout changed")
			}
			f.install(t, false)
			if read(t, f.destination, "app.js") != "local-only\n" {
				t.Fatal("reviewed change missing")
			}
		})
	}
}

func TestExplicitPublishedBaseAbsentLocally(t *testing.T) {
	f := newFixture(t, false)
	remote := publishedClone(t, f)
	write(t, remote, "NEWS.md", "new upstream")
	base := commit(t, remote, "NEWS.md")
	var out strings.Builder
	check(t, run(ctx, []string{"create", "--source", f.source, "--base", base, "--dry-run"}, &out, nil))
	if !strings.Contains(out.String(), base) || !strings.Contains(out.String(), "NEWS.md") {
		t.Fatal(out.String())
	}
	_, err := prepareCreateSource(ctx, CreateOptions{Source: f.source, Base: strings.Repeat("0", 40)}, nil)
	fails(t, err, "not confirmed available")
}

func TestForkSourceChoice(t *testing.T) {
	f := newFixture(t, true)
	original := "https://github.com/original/app.git"
	g(t, f.source, "remote", "add", "upstream", original)
	g(t, f.root, "config", "--global", "--add", "url."+filepath.ToSlash(f.source)+".insteadOf", original)
	_, err := prepareCreateSource(ctx, CreateOptions{Source: f.source}, nil)
	fails(t, err, "multiple source repositories")
	out, err := reviewCreate(t, f, "1\ny\n")
	check(t, err)
	if !strings.Contains(out, "fork and upstream may differ") {
		t.Fatal(out)
	}
	p, err := LoadPack(f.pack)
	check(t, err)
	if p.Manifest.Upstream.Repository != original {
		t.Fatal(p.Manifest.Upstream)
	}
	prepared, err := prepareCreateSource(ctx, CreateOptions{Source: f.source, Upstream: repo}, nil)
	check(t, err)
	check(t, prepared.close())
}

func TestCreateRefusesEnvironmentInSourceRevision(t *testing.T) {
	f := newFixture(t, true)
	write(t, f.source, ".radius/radius-environment.bicep", "author state")
	commit(t, f.source, ".radius/radius-environment.bicep")
	_, err := reviewCreate(t, f, "y\n")
	fails(t, err, "upstream contains non-portable Radius environment material")
	notCreated(t, f.pack)
}

func TestBuildSourceReviewWarnings(t *testing.T) {
	f := newFixture(t, false)
	write(t, f.source, "app.js", "adapted\n")
	old := strings.Repeat("a", 40)
	write(t, f.source, ".radius/app.bicep", app+"\nvar source = 'git::"+repo+"?ref="+old+"'")
	p := f.plan(t, "app.js")
	warnings := strings.Join(p.BuildWarnings, "\n")
	if !strings.Contains(warnings, "consumers clone") || !strings.Contains(warnings, "pack changes app.js") {
		t.Fatal(warnings)
	}
	original := p.Approval
	p.BuildWarnings = nil
	_, err := WritePack(p, f.pack, original)
	fails(t, err, "approval does not match")
	assets := map[string][]byte{".radius/app.bicep": []byte(
		"var other = 'git::https://github.com/external/service.git?ref=" + old + "'\n" +
			"var mutable = 'git::" + repo + "//src?ref=main'\n" +
			"var dynamic = 'git::" + repo + "?ref=${revision}'\n")}
	warnings = strings.Join(buildSourceWarnings(p.Manifest, assets), "\n")
	for _, message := range []string{"another repository", "full immutable commit", "dynamic Git build source"} {
		if !strings.Contains(warnings, message) {
			t.Fatal(warnings)
		}
	}
}

func TestTTYAndCancelledPrompt(t *testing.T) {
	file, err := os.Open(os.DevNull)
	check(t, err)
	defer file.Close()
	if terminalUI(file, os.Stdout) != nil {
		t.Fatal("null input treated as terminal")
	}
	if terminalUI(os.Stdin, io.Discard) != nil {
		t.Fatal("redirected output treated as terminal")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	ui := &createUI{bufio.NewReader(panicReader{}), io.Discard}
	_, err = ui.confirm(cancelled, "Create?")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ui = &createUI{bufio.NewReader(strings.NewReader(strings.Repeat("y", 5000))), io.Discard}
	_, err = ui.confirm(ctx, "Create?")
	fails(t, err, "could not read")
}

func TestNativePipedYesCannotApprove(t *testing.T) {
	f := newFixture(t, true)
	executable := filepath.Join(f.root, "radpack-test-command")
	// Cross-platform os/exec requires .exe on Windows.
	if filepath.Separator == '\\' {
		executable += ".exe"
	}
	build := exec.Command("go", "build", "-buildvcs=false", "-o", executable, "../../cmd/radpack")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", output, err)
	}
	command := exec.Command(executable, "create", "--source", f.source, "--output", f.pack)
	command.Stdin = bytes.NewBufferString("yes\n")
	output, err := command.CombinedOutput()
	check(t, err)
	if !strings.Contains(string(output), "No files written") || strings.Contains(string(output), "Create this pack?") {
		t.Fatal(string(output))
	}
	notCreated(t, f.pack)
}
