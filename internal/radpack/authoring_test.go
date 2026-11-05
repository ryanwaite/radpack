package radpack

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func newAuthoringFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, false)
	// Only the application source has been committed; modeling happens later.
	return f
}

func authorJSON(t *testing.T, args ...string) AuthoringReport {
	t.Helper()
	var out strings.Builder
	check(t, run(ctx, args, &out, nil))
	var report AuthoringReport
	check(t, json.Unmarshal([]byte(out.String()), &report))
	return report
}

func prepareURL(t *testing.T, f *fixture, extra ...string) AuthoringReport {
	t.Helper()
	args := append([]string{"create", repo, "--directory", f.destination, "--output", f.pack, "--json", "--no-fork"}, extra...)
	report := authorJSON(t, args...)
	if report.Status != "awaiting-radification" || report.Application == nil || report.Application.Commit != f.base {
		t.Fatal(report)
	}
	return report
}

func radify(t *testing.T, root string) {
	t.Helper()
	write(t, root, ".radius/app.bicep", app)
	write(t, root, ".radius/bicepconfig.json", config)
}

func resumeReviewJSON(t *testing.T, source string, extra ...string) CreateReport {
	t.Helper()
	args := append([]string{"create", "--resume", source, "--json"}, extra...)
	var out strings.Builder
	check(t, run(ctx, args, &out, nil))
	var report CreateReport
	check(t, json.Unmarshal([]byte(out.String()), &report))
	if report.Review == nil {
		t.Fatalf("expected review, got %s", out.String())
	}
	return report
}

func TestURLCreateResumeAndSave(t *testing.T) {
	f := newAuthoringFixture(t)
	initial := prepareURL(t, f, "--name", "community-app")
	if initial.Credentials != "not-inspected" || initial.Deployment != "not-performed" || initial.Publication != "not-performed" {
		t.Fatal(initial)
	}
	if g(t, f.destination, "status", "--porcelain") != "" {
		t.Fatal("authoring state dirtied the application checkout")
	}
	if g(t, f.destination, "config", "--get", "remote.origin.url") != repo {
		t.Fatal("wrong origin")
	}
	state, err := loadAuthoring(ctx, f.destination)
	check(t, err)
	if state == nil || state.record.Output != f.pack {
		t.Fatal(state)
	}
	notCreated(t, f.pack)
	radify(t, f.destination)
	review := resumeReviewJSON(t, f.destination)
	if review.Status != "awaiting-review" || review.Review.Manifest.Upstream.Commit != f.base ||
		review.Review.Manifest.Name != "community-app" || review.Output != f.pack {
		t.Fatal(review)
	}
	saved := resumeReviewJSON(t, f.destination, "--approve", review.ReviewSHA256)
	if saved.Status != "saved" {
		t.Fatal(saved)
	}
	pack, err := LoadPack(f.pack)
	check(t, err)
	for _, file := range pack.Manifest.Files {
		if strings.Contains(file.Path, "authoring") || strings.HasPrefix(file.Path, ".git") {
			t.Fatal("local authoring state escaped into pack")
		}
	}
	final := authorJSON(t, "create", "--resume", f.destination, "--json", "--status")
	if final.Status != "saved" || final.PackSHA256 != pack.Digest {
		t.Fatal(final)
	}
	again := authorJSON(t, "create", "--resume", f.destination, "--json")
	if again.Status != "saved" {
		t.Fatal("resume tried to recreate saved output")
	}
}

func TestURLCreatePreservesOriginalBaseline(t *testing.T) {
	f := newAuthoringFixture(t)
	prepareURL(t, f)
	write(t, f.source, "NEWS.md", "upstream advanced")
	newUpstream := commit(t, f.source, "NEWS.md")
	radify(t, f.destination)
	commit(t, f.destination, ".radius")
	review := resumeReviewJSON(t, f.destination)
	if review.Review.Manifest.Upstream.Commit != f.base || newUpstream == f.base {
		t.Fatal("resume followed new upstream or prepared HEAD")
	}
	if review.Review.Manifest.Files[0].Operation != "add" {
		t.Fatal("committed preparation no longer captured")
	}
	var out strings.Builder
	err := run(ctx, []string{"create", "--resume", f.destination, "--base", newUpstream, "--json"}, &out, nil)
	fails(t, err, "preserves the recorded base")
}

func TestURLCreateWorktreeSelection(t *testing.T) {
	f := newAuthoringFixture(t)
	prepareURL(t, f)
	worktree := filepath.Join(f.root, "copilot-worktree")
	g(t, f.destination, "worktree", "add", "--quiet", "-b", "author-model", worktree, f.base)
	radify(t, worktree)
	status := authorJSON(t, "create", "--resume", f.destination, "--status", "--json")
	if status.Status != "awaiting-worktree-selection" || len(status.Worktrees) != 2 {
		t.Fatal(status)
	}
	model := authorJSON(t, "create", "--resume", f.destination, "--source", worktree, "--status", "--json")
	if model.Status != "ready-for-review" {
		t.Fatal(model)
	}
	review := resumeReviewJSON(t, worktree)
	if review.Status != "awaiting-review" {
		t.Fatal(review)
	}
	resumeReviewJSON(t, f.destination, "--source", worktree, "--approve", review.ReviewSHA256)
	final := authorJSON(t, "create", "--resume", f.destination, "--status", "--json")
	canonical, err := canonicalDirectory(worktree)
	check(t, err)
	if final.Worktree != canonical || final.Status != "saved" {
		t.Fatal(final)
	}
	if g(t, f.destination, "status", "--porcelain") != "" {
		t.Fatal("modified original clone instead of worktree")
	}
}

func TestURLCreateRejectsUnrelatedWorktree(t *testing.T) {
	f := newAuthoringFixture(t)
	prepareURL(t, f)
	radify(t, f.source)
	var out strings.Builder
	err := run(ctx, []string{"create", "--resume", f.destination, "--source", f.source, "--json"}, &out, nil)
	fails(t, err, "linked worktrees")
	var report AuthoringReport
	check(t, json.Unmarshal([]byte(out.String()), &report))
	if report.Status != "blocked" {
		t.Fatal(report)
	}
}

func TestURLCreatePauseCancelAndEOF(t *testing.T) {
	for _, answer := range []string{"", "q\n", "quit\n", "\n"} {
		t.Run("answer-"+answer, func(t *testing.T) {
			f := newAuthoringFixture(t)
			var out strings.Builder
			ui := &createUI{bufio.NewReader(strings.NewReader(answer)), &out}
			check(t, runWithUI(ctx, []string{"create", repo, "--directory", f.destination, "--output", f.pack, "--no-fork"}, &out, nil, ui))
			state, err := loadAuthoring(ctx, f.destination)
			check(t, err)
			if state == nil || !strings.Contains(out.String(), "Go Radify your app") {
				t.Fatal(out.String())
			}
			notCreated(t, f.pack)
			radify(t, f.destination)
			review := resumeReviewJSON(t, f.destination)
			if review.Status != "awaiting-review" {
				t.Fatal("cannot resume after pause")
			}
		})
	}
}

func TestURLCreateEnterReviewsNewModel(t *testing.T) {
	f := newAuthoringFixture(t)
	var out strings.Builder
	reader := &mutatingReader{reader: strings.NewReader("\ny\n"), mutate: func() {
		radify(t, f.destination)
	}}
	ui := &createUI{bufio.NewReader(reader), &out}
	check(t, runWithUI(ctx, []string{"create", repo, "--directory", f.destination, "--output", f.pack, "--no-fork"}, &out, nil, ui))
	if !strings.Contains(out.String(), "Create this pack? [y/N]") || !strings.Contains(out.String(), "Created ") {
		t.Fatal(out.String())
	}
	pack, err := LoadPack(f.pack)
	check(t, err)
	if pack.Manifest.Upstream.Commit != f.base {
		t.Fatal(pack.Manifest)
	}
}

func TestURLCreateWorktreeAddedDuringPause(t *testing.T) {
	f := newAuthoringFixture(t)
	worktree := filepath.Join(f.root, "a-worktree")
	var out strings.Builder
	reader := &mutatingReader{reader: strings.NewReader("\n1\ny\n"), mutate: func() {
		g(t, f.destination, "worktree", "add", "--quiet", "-b", "model", worktree, f.base)
		radify(t, worktree)
	}}
	ui := &createUI{bufio.NewReader(reader), &out}
	check(t, runWithUI(ctx, []string{"create", repo, "--directory", f.destination, "--output", f.pack, "--no-fork"}, &out, nil, ui))
	if !strings.Contains(out.String(), "Choose the checkout number") {
		t.Fatal("worktree created during pause was not offered")
	}
	_, err := LoadPack(f.pack)
	check(t, err)
}

func TestURLResumeStatusNeverSavesOrPrompts(t *testing.T) {
	f := newAuthoringFixture(t)
	prepareURL(t, f)
	before := authorJSON(t, "create", "--resume", f.destination, "--status", "--json")
	if before.Status != "awaiting-radification" {
		t.Fatal(before)
	}
	radify(t, f.destination)
	var out strings.Builder
	ui := &createUI{bufio.NewReader(panicReader{}), &out}
	check(t, runWithUI(ctx, []string{"create", "--resume", f.destination, "--status", "--json"}, &out, nil, ui))
	var status AuthoringReport
	check(t, json.Unmarshal([]byte(out.String()), &status))
	if status.Status != "ready-for-review" {
		t.Fatal(status)
	}
	notCreated(t, f.pack)
}

func TestURLAuthoringPersistsSelections(t *testing.T) {
	f := newAuthoringFixture(t)
	prepareURL(t, f, "--recipe-pack", ".radius/packs/cloud.bicep", "--include", "app.js", "--radius-version", "0.61.0")
	radify(t, f.destination)
	write(t, f.destination, ".radius/packs/cloud.bicep", reusablePack)
	write(t, f.destination, "app.js", "adapted")
	review := resumeReviewJSON(t, f.destination)
	if review.Review.Manifest.FormatVersion != 2 || *review.Review.Manifest.RadiusVersion != "0.61.0" {
		t.Fatal(review)
	}
	if !reflect.DeepEqual(review.Review.Manifest.Integration.RecipePacks, []string{".radius/packs/cloud.bicep"}) {
		t.Fatal(review.Review.Manifest.Integration)
	}
	found := false
	for _, file := range review.Review.Manifest.Files {
		if file.Path == "app.js" && file.Operation == "replace" {
			found = true
		}
	}
	if !found {
		t.Fatal("explicit application adaptation lost on resume")
	}
}

func TestURLCreateRefusesExistingDestinationAndUnsafeInputs(t *testing.T) {
	f := newAuthoringFixture(t)
	write(t, f.destination, "mine.txt", "keep")
	var out strings.Builder
	err := run(ctx, []string{"create", repo, "--directory", f.destination, "--json"}, &out, nil)
	fails(t, err, "already exists")
	if read(t, f.destination, "mine.txt") != "keep" {
		t.Fatal("existing directory changed")
	}
	for _, args := range [][]string{
		{"create", repo, "--source", f.source},
		{"create", repo, "--resume", f.source},
		{"create", repo, "--upstream", "other/repo"},
		{"create", repo, "--approve", strings.Repeat("a", 64)},
		{"create", repo, "--dry-run"},
		{"create", repo, "--status"},
		{"create", "--directory", f.destination},
		{"create", "--status"},
		{"create", "--resume", f.destination, "--status", "--approve", "x"},
		{"create", "--resume", f.destination, "--directory", "other"},
	} {
		if run(ctx, args, &out, nil) == nil {
			t.Fatal("invalid combination accepted", args)
		}
	}
	out.Reset()
	err = run(ctx, []string{"create", "https://user:do-not-print@github.com/owner/repo", "--json"}, &out, nil)
	if err == nil || strings.Contains(out.String()+err.Error(), "do-not-print") {
		t.Fatal("unsafe URL or leaked value")
	}
}

func TestURLCreateCloneFailureCleansOnlyNewDestination(t *testing.T) {
	f := newAuthoringFixture(t)
	var out strings.Builder
	err := run(ctx, []string{"create", repo, "--base", strings.Repeat("0", 40), "--directory", f.destination, "--json"}, &out, nil)
	fails(t, err, "Git fetch failed")
	notCreated(t, f.destination)
	if read(t, f.source, "app.js") != "upstream\n" {
		t.Fatal("original repository changed")
	}
}

func TestAuthoringStateGuards(t *testing.T) {
	for _, mode := range []string{"state-missing", "wrong-common", "wrong-repository", "record-link", "state-changed-during-review", "lock"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "record-link" && runtime.GOOS == "windows" {
				t.Skip("symlink permissions")
			}
			f := newAuthoringFixture(t)
			prepareURL(t, f)
			radify(t, f.destination)
			state, err := loadAuthoring(ctx, f.destination)
			check(t, err)
			switch mode {
			case "state-missing":
				check(t, os.Remove(filepath.Join(state.directory, "request.json")))
			case "wrong-common":
				state.record.CommonDirectory = "wrong"
				writeJSON(t, state.directory, "request.json", state.record)
			case "wrong-repository":
				state.record.Upstream.Repository = "https://token@github.com/private/repo"
				writeJSON(t, state.directory, "request.json", state.record)
			case "record-link":
				check(t, os.Rename(filepath.Join(state.directory, "request.json"), filepath.Join(state.directory, "actual.json")))
				check(t, os.Symlink(filepath.Join(state.directory, "actual.json"), filepath.Join(state.directory, "request.json")))
			case "lock":
				write(t, state.directory, "review.lock", "another review")
			case "state-changed-during-review":
				var out strings.Builder
				reader := &mutatingReader{reader: strings.NewReader("y\n"), mutate: func() {
					state.record.Name = "changed"
					writeJSON(t, state.directory, "request.json", state.record)
				}}
				err := runWithUI(ctx, []string{"create", "--resume", f.destination}, &out, nil,
					&createUI{bufio.NewReader(reader), &out})
				fails(t, err, "checkpoint")
				_, err = LoadPack(f.pack)
				check(t, err)
				return
			}
			var out strings.Builder
			err = run(ctx, []string{"create", "--resume", f.destination, "--json"}, &out, nil)
			if err == nil {
				t.Fatal("invalid local state accepted")
			}
			notCreated(t, f.pack)
		})
	}
}

func TestResumeStaleApprovalAndSavedIntegrity(t *testing.T) {
	f := newAuthoringFixture(t)
	prepareURL(t, f)
	radify(t, f.destination)
	review := resumeReviewJSON(t, f.destination)
	write(t, f.destination, ".radius/app.bicep", app+"\n// changed")
	var out strings.Builder
	err := run(ctx, []string{"create", "--resume", f.destination, "--approve", review.ReviewSHA256, "--json"}, &out, nil)
	fails(t, err, "approval does not match")
	notCreated(t, f.pack)
	newReview := resumeReviewJSON(t, f.destination)
	resumeReviewJSON(t, f.destination, "--approve", newReview.ReviewSHA256)
	write(t, f.pack, "overlay/.radius/app.bicep", "tampered")
	out.Reset()
	err = run(ctx, []string{"create", "--resume", f.destination, "--status", "--json"}, &out, nil)
	fails(t, err, "saved pack is unavailable or modified")
}

func TestAuthoringCancellationAndReadOnlyWaiting(t *testing.T) {
	f := newAuthoringFixture(t)
	prepareURL(t, f)
	var out strings.Builder
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	err := runWithUI(cancelled, []string{"create", "--resume", f.destination}, &out, nil,
		&createUI{bufio.NewReader(panicReader{}), &out})
	if err == nil {
		t.Fatal("cancelled workflow proceeded")
	}
	// Git cancellation may be surfaced through its deliberately redacted error.
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "Git") {
		t.Fatal(err)
	}
	state, err := loadAuthoring(ctx, f.destination)
	check(t, err)
	if state == nil {
		t.Fatal("cancellation lost resume state")
	}
}

func TestNativeURLCreateDefaultsAndRestart(t *testing.T) {
	f := newAuthoringFixture(t)
	executable := filepath.Join(f.root, "radpack-cli")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	build := exec.Command("go", "build", "-buildvcs=false", "-o", executable, "../../cmd/radpack")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", output, err)
	}
	command := exec.Command(executable, "create", repo, "--json", "--no-fork")
	command.Dir = f.root
	command.Stdin = strings.NewReader("yes\n")
	output, err := command.CombinedOutput()
	check(t, err)
	var started AuthoringReport
	check(t, json.Unmarshal(output, &started))
	source := filepath.Join(f.root, "app")
	expected, err := canonicalDirectory(source)
	check(t, err)
	if started.Source != expected || started.Status != "awaiting-radification" ||
		started.Output != filepath.Join(f.root, "contrib", "app") {
		t.Fatal(started)
	}
	radify(t, source)
	// A new process, from a different current directory, uses durable paths.
	resume := exec.Command(executable, "create", "--resume", source, "--json")
	resume.Dir = f.source
	output, err = resume.CombinedOutput()
	check(t, err)
	var preview CreateReport
	check(t, json.Unmarshal(output, &preview))
	if preview.Status != "awaiting-review" || preview.Output != started.Output {
		t.Fatal(preview)
	}
}

func TestURLStartExistingModelStillPauses(t *testing.T) {
	f := newFixture(t, true)
	prepareURL(t, f)
	notCreated(t, f.pack)
	status := authorJSON(t, "create", "--resume", f.destination, "--status", "--json")
	if status.Status != "ready-for-review" {
		t.Fatal(status)
	}
	review := resumeReviewJSON(t, f.destination)
	for _, file := range review.Review.Manifest.Files {
		if file.Operation != "retain" {
			t.Fatal("already-published model reconstructed as overlay")
		}
	}
}

func TestURLStartExplicitBaselineAndOutputOverride(t *testing.T) {
	f := newAuthoringFixture(t)
	base := f.base
	write(t, f.source, "new.txt", "new upstream")
	commit(t, f.source, "new.txt")
	prepareURL(t, f, "--base", base)
	if g(t, f.destination, "rev-parse", "HEAD") != base {
		t.Fatal("explicit source revision not honored")
	}
	radify(t, f.destination)
	override := filepath.Join(f.root, "alternate-pack")
	review := resumeReviewJSON(t, f.destination, "--output", override)
	resumeReviewJSON(t, f.destination, "--output", override, "--approve", review.ReviewSHA256)
	status := authorJSON(t, "create", "--resume", f.destination, "--status", "--json")
	if status.Output != override || status.Status != "saved" {
		t.Fatal(status)
	}
	notCreated(t, f.pack)
}

func TestResumePolicyAndMissingModelApproval(t *testing.T) {
	f := newAuthoringFixture(t)
	prepareURL(t, f)
	var out strings.Builder
	err := run(ctx, []string{"create", "--resume", f.destination, "--approve", strings.Repeat("a", 64), "--json"}, &out, nil)
	fails(t, err, "no application model")
	var waiting AuthoringReport
	check(t, json.Unmarshal([]byte(out.String()), &waiting))
	if waiting.Status != "blocked" {
		t.Fatal(waiting)
	}
	radify(t, f.destination)
	write(t, f.destination, ".env", "PASSWORD=do-not-export")
	out.Reset()
	err = run(ctx, []string{"create", "--resume", f.destination, "--include", ".env", "--json"}, &out, nil)
	fails(t, err, "export blocked")
	var result CreateReport
	check(t, json.Unmarshal([]byte(out.String()), &result))
	if result.Stage != "policy" {
		t.Fatal(result)
	}
	notCreated(t, f.pack)
}

func TestResumeCheckpointErrorIsHonest(t *testing.T) {
	f := newAuthoringFixture(t)
	prepareURL(t, f)
	radify(t, f.destination)
	state, err := loadAuthoring(ctx, f.destination)
	check(t, err)
	var out strings.Builder
	ui := &createUI{bufio.NewReader(&mutatingReader{
		reader: strings.NewReader("y\n"),
		mutate: func() { write(t, state.directory, "saved.json", "invalid interrupted checkpoint") },
	}), &out}
	err = runWithUI(ctx, []string{"create", "--resume", f.destination}, &out, nil, ui)
	fails(t, err, "pack saved")
	_, err = LoadPack(f.pack)
	check(t, err)
	// No silent success and no second output on another resume.
	out.Reset()
	err = run(ctx, []string{"create", "--resume", f.destination, "--status", "--json"}, &out, nil)
	if err == nil {
		t.Fatal("invalid checkpoint silently ignored")
	}
}
