package radpack

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func isolateHandoffHome(t *testing.T, f *fixture) {
	t.Helper()
	home, err := canonicalDirectory(f.root)
	check(t, err)
	home = filepath.Join(home, "home")
	check(t, os.Mkdir(home, 0700))
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func prepareHandoffFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t, false)
	isolateHandoffHome(t, f)
	write(t, f.source, "app.js", "pack adaptation\n")
	check(t, os.Remove(filepath.Join(f.source, "obsolete.txt")))
	f.publish(t, "app.js", "obsolete.txt")
	r, err := InstallApplication(ctx, f.pack, ApplicationOptions{Directory: f.destination, Desktop: true}, nil)
	check(t, err)
	if r.Status != "handoff-pending" {
		t.Fatal(r)
	}
	worktree := filepath.Join(f.root, "app-worktree")
	g(t, f.destination, "worktree", "add", "--quiet", "-b", "app-session", worktree, f.base)
	worktree, err = canonicalDirectory(worktree)
	check(t, err)
	return f, worktree
}

func TestDesktopPreparationAndConsentedApply(t *testing.T) {
	f, worktree := prepareHandoffFixture(t)
	if read(t, f.destination, "app.js") != "upstream\n" {
		t.Fatal("overlay applied to original clone")
	}
	if g(t, f.destination, "status", "--porcelain") != "" {
		t.Fatal("source clone is dirty")
	}
	status, err := HandoffStatusFor(ctx, worktree)
	check(t, err)
	if status.Status != "pending" || status.ModelPath != ".radius/app.bicep" || status.ModelSHA256 != hash([]byte(app)) ||
		status.WorktreeDirectory != worktree || !hashPattern.MatchString(status.ReviewSHA256) {
		t.Fatal(status)
	}
	if _, err := os.Stat(filepath.Join(worktree, ".radius/app.bicep")); !os.IsNotExist(err) {
		t.Fatal("status mutated worktree")
	}
	applied, err := ApplyHandoff(ctx, worktree, status.RequestID, status.ReviewSHA256)
	check(t, err)
	if applied.Status != "applied" || applied.ReviewSHA256 != status.ReviewSHA256 ||
		read(t, worktree, "app.js") != "pack adaptation\n" {
		t.Fatal(applied)
	}
	if _, err := os.Stat(filepath.Join(worktree, "obsolete.txt")); !os.IsNotExist(err) {
		t.Fatal("delete not applied")
	}
	_, err = VerifyInstallation(ctx, "", worktree)
	check(t, err)
	status, err = HandoffStatusFor(ctx, worktree)
	check(t, err)
	if status.Status != "applied" {
		t.Fatal(status)
	}
	if status.ReviewSHA256 != applied.ReviewSHA256 {
		t.Fatal("applied status lost approved review identity")
	}
	again, err := ApplyHandoff(ctx, worktree, status.RequestID, "unused-after-success")
	check(t, err)
	if again.Status != "applied" {
		t.Fatal(again)
	}
	if read(t, f.destination, "app.js") != "upstream\n" || g(t, f.destination, "status", "--porcelain") != "" {
		t.Fatal("source clone modified")
	}
}

func TestHandoffRequiresExactReviewAndWorktree(t *testing.T) {
	f, worktree := prepareHandoffFixture(t)
	status, err := HandoffStatusFor(ctx, worktree)
	check(t, err)
	_, err = ApplyHandoff(ctx, worktree, strings.Repeat("0", 32), status.ReviewSHA256)
	fails(t, err, "request ID")
	_, err = ApplyHandoff(ctx, worktree, status.RequestID, strings.Repeat("0", 64))
	fails(t, err, "approval")
	original, err := HandoffStatusFor(ctx, f.destination)
	check(t, err)
	_, err = ApplyHandoff(ctx, f.destination, original.RequestID, original.ReviewSHA256)
	fails(t, err, "app-created worktree")
	other := filepath.Join(f.root, "other")
	check(t, os.Mkdir(other, 0755))
	check(t, cloneRevision(ctx, other, repo, f.base))
	unrelated, err := HandoffStatusFor(ctx, other)
	check(t, err)
	if unrelated.Status != "none" {
		t.Fatal("matched by URL rather than clone identity")
	}
	_, err = ApplyHandoff(ctx, other, status.RequestID, status.ReviewSHA256)
	fails(t, err, "no pending")
	_, err = ApplyHandoff(ctx, worktree, status.RequestID, status.ReviewSHA256)
	check(t, err)
}

func TestHandoffRefusesDirtyAndChangedWorktrees(t *testing.T) {
	for _, file := range []string{"app.js", "untracked.txt", "ignored.txt"} {
		t.Run(file, func(t *testing.T) {
			_, worktree := prepareHandoffFixture(t)
			before, err := HandoffStatusFor(ctx, worktree)
			check(t, err)
			write(t, worktree, file, "user change\n")
			after, err := HandoffStatusFor(ctx, worktree)
			check(t, err)
			if after.Status != "blocked" {
				t.Fatal(after)
			}
			_, err = ApplyHandoff(ctx, worktree, before.RequestID, before.ReviewSHA256)
			fails(t, err, "blocked")
			if read(t, worktree, file) != "user change\n" {
				t.Fatal("user change overwritten")
			}
		})
	}
}

func TestHandoffWrongCommitAndClaim(t *testing.T) {
	f, worktree := prepareHandoffFixture(t)
	write(t, worktree, "new.txt", "commit change")
	commit(t, worktree, "new.txt")
	status, err := HandoffStatusFor(ctx, worktree)
	check(t, err)
	if status.Status != "blocked" {
		t.Fatal(status)
	}
	second := filepath.Join(f.root, "correct")
	g(t, f.destination, "worktree", "add", "--quiet", "-b", "correct", second, f.base)
	status, err = HandoffStatusFor(ctx, second)
	check(t, err)
	_, err = ApplyHandoff(ctx, second, status.RequestID, status.ReviewSHA256)
	check(t, err)
	third := filepath.Join(f.root, "third")
	g(t, f.destination, "worktree", "add", "--quiet", "-b", "third", third, f.base)
	status, err = HandoffStatusFor(ctx, third)
	check(t, err)
	if status.Status != "blocked" || !strings.Contains(strings.Join(status.Warnings, " "), "another worktree") {
		t.Fatal(status)
	}
}

func TestHandoffTamperAndExpired(t *testing.T) {
	for _, mode := range []string{"candidate", "request", "expired", "interrupted"} {
		t.Run(mode, func(t *testing.T) {
			_, worktree := prepareHandoffFixture(t)
			h, err := loadHandoff(ctx, worktree)
			check(t, err)
			switch mode {
			case "candidate":
				write(t, filepath.Join(h.directory, "candidate"), "app.js", "tampered")
			case "request":
				h.record.CommonDirectory = "wrong"
				writeJSON(t, h.directory, "request.json", h.record)
			case "expired":
				h.record.ExpiresAt = "2000-01-01T00:00:00Z"
				writeJSON(t, h.directory, "request.json", h.record)
			case "interrupted":
				check(t, savePrivateJSON(h.directory, "claim.json", handoffClaim{h.record.RequestID, worktree, h.status.ReviewSHA256}))
			}
			status, err := HandoffStatusFor(ctx, worktree)
			if mode == "expired" || mode == "interrupted" {
				check(t, err)
				if status.Status != "blocked" {
					t.Fatal(status)
				}
			} else if err == nil {
				t.Fatal("tampering accepted")
			}
			if read(t, worktree, "app.js") != "upstream\n" {
				t.Fatal("status changed files")
			}
		})
	}
}

func TestDesktopOpenFailurePreservesSourceAndResume(t *testing.T) {
	f := newFixture(t, false)
	isolateHandoffHome(t, f)
	f.publish(t)
	exe, err := os.Executable()
	check(t, err)
	t.Setenv("RADPACK_GITHUB_COMMAND", exe)
	t.Setenv("RADPACK_DESKTOP_TEST_HELPER", "fail")
	var output strings.Builder
	err = run(ctx, []string{"install", f.pack, "--directory", f.destination}, &output, nil)
	fails(t, err, "preserved")
	if read(t, f.destination, "app.js") != "upstream\n" {
		t.Fatal("source not preserved")
	}
	status, err := HandoffStatusFor(ctx, f.destination)
	check(t, err)
	if status.Status != "pending" {
		t.Fatal(status)
	}
	t.Setenv("RADPACK_DESKTOP_TEST_HELPER", "success")
	log := filepath.Join(f.root, "opened")
	t.Setenv("RADPACK_DESKTOP_TEST_OUTPUT", log)
	check(t, run(ctx, []string{"open", "--directory", f.destination}, &output, nil))
	actual, err := os.ReadFile(log)
	check(t, err)
	if string(actual) != status.SourceDirectory {
		t.Fatal("wrong directory opened")
	}
}

func TestNoOpenHasNoDesktopSideEffects(t *testing.T) {
	f := newFixture(t, false)
	isolateHandoffHome(t, f)
	f.publish(t)
	t.Setenv("RADPACK_GITHUB_COMMAND", "not-an-absolute-command")
	var output strings.Builder
	check(t, run(ctx, []string{"install", f.pack, "--directory", f.destination, "--no-open"}, &output,
		func(context.Context) (string, error) { return "0.60.0", nil }))
	_, err := VerifyInstallation(ctx, "", f.destination)
	check(t, err)
	home, err := handoffHome()
	check(t, err)
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("no-open wrote handoff state")
	}
}

func TestDesktopVersionWarningIsNonfatal(t *testing.T) {
	f := newFixture(t, false)
	isolateHandoffHome(t, f)
	f.publish(t)
	var messages []string
	calls := 0
	result, err := InstallApplication(ctx, f.pack, ApplicationOptions{
		Directory: f.destination, Desktop: true,
		VersionProbe: func(context.Context) (string, error) { calls++; return "9.0.0", nil },
	}, func(s string) { messages = append(messages, s) })
	check(t, err)
	if result.Status != "handoff-pending" || calls != 1 ||
		!strings.Contains(strings.Join(messages, "\n"), "local CLI reports 9.0.0") {
		t.Fatal(result, calls, messages)
	}
}

func TestHandoffJSONCLI(t *testing.T) {
	_, worktree := prepareHandoffFixture(t)
	var output strings.Builder
	check(t, run(ctx, []string{"handoff", "status", "--directory", worktree, "--json"}, &output, nil))
	var status HandoffStatus
	check(t, json.Unmarshal([]byte(output.String()), &status))
	if status.Status != "pending" {
		t.Fatal(output.String())
	}
	output.Reset()
	check(t, run(ctx, []string{"handoff", "apply", "--directory", worktree, "--request-id", status.RequestID, "--approve", status.ReviewSHA256, "--json"}, &output, nil))
	check(t, json.Unmarshal([]byte(output.String()), &status))
	if status.Status != "applied" {
		t.Fatal(output.String())
	}
}

func TestHandoffSymlinkAndPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions and symlink fixture")
	}
	_, worktree := prepareHandoffFixture(t)
	h, err := loadHandoff(ctx, worktree)
	check(t, err)
	check(t, os.Chmod(h.directory, 0777))
	_, err = HandoffStatusFor(ctx, worktree)
	fails(t, err, "writable")
	check(t, os.Chmod(h.directory, 0700))
	check(t, os.Rename(filepath.Join(h.directory, "request.json"), filepath.Join(h.directory, "real.json")))
	check(t, os.Symlink(filepath.Join(h.directory, "real.json"), filepath.Join(h.directory, "request.json")))
	_, err = HandoffStatusFor(ctx, worktree)
	fails(t, err, "symbolic")
}

func TestLatestDesktopHandoff(t *testing.T) {
	f := newFixture(t, false)
	isolateHandoffHome(t, f)
	write(t, f.source, "app.js", "pack adaptation")
	f.publish(t, "app.js")
	write(t, f.source, "app.js", "new upstream")
	latest := commit(t, f.source, "app.js")
	_, err := InstallApplication(ctx, f.pack, ApplicationOptions{Directory: f.destination, Latest: true, Adapt: true, Desktop: true}, nil)
	check(t, err)
	worktree := filepath.Join(f.root, "latest")
	g(t, f.destination, "worktree", "add", "--quiet", "-b", "latest", worktree, latest)
	status, err := HandoffStatusFor(ctx, worktree)
	check(t, err)
	if status.InstallationStatus != "adaptation-required" {
		t.Fatal(status)
	}
	for _, file := range status.Files {
		if file.Path == "app.js" && file.Operation != "retain" {
			t.Fatal("review should describe the preserved latest file, not an unapplied reference replacement")
		}
	}
	status, err = ApplyHandoff(ctx, worktree, status.RequestID, status.ReviewSHA256)
	check(t, err)
	if status.InstallationStatus != "adaptation-required" || read(t, worktree, "app.js") != "new upstream" {
		t.Fatal(status)
	}
}

func TestDesktopResumeAppliedWorktree(t *testing.T) {
	f, worktree := prepareHandoffFixture(t)
	status, err := HandoffStatusFor(ctx, worktree)
	check(t, err)
	_, err = ApplyHandoff(ctx, worktree, status.RequestID, status.ReviewSHA256)
	check(t, err)
	exe, err := os.Executable()
	check(t, err)
	t.Setenv("RADPACK_GITHUB_COMMAND", exe)
	t.Setenv("RADPACK_DESKTOP_TEST_HELPER", "success")
	log := filepath.Join(f.root, "opened")
	t.Setenv("RADPACK_DESKTOP_TEST_OUTPUT", log)
	check(t, resumeDesktop(ctx, f.destination))
	content, err := os.ReadFile(log)
	check(t, err)
	if string(content) != worktree {
		t.Fatal("resume opened source instead of claimed worktree")
	}
}

func TestHandoffCloneIdentityAndMissingModel(t *testing.T) {
	t.Run("clone-identity", func(t *testing.T) {
		_, worktree := prepareHandoffFixture(t)
		h, err := loadHandoff(ctx, worktree)
		check(t, err)
		check(t, os.Remove(filepath.Join(h.record.CommonDirectory, "radpack-handoff-id")))
		_, err = HandoffStatusFor(ctx, worktree)
		fails(t, err, "clone handoff identity changed")
	})
	t.Run("latest-deleted-model", func(t *testing.T) {
		f := newFixture(t, true)
		isolateHandoffHome(t, f)
		f.publish(t)
		check(t, os.Remove(filepath.Join(f.source, ".radius/app.bicep")))
		latest := commit(t, f.source, ".radius/app.bicep")
		_, err := InstallApplication(ctx, f.pack, ApplicationOptions{Directory: f.destination, Latest: true, Adapt: true, Desktop: true}, nil)
		check(t, err)
		worktree := filepath.Join(f.root, "missing-model")
		g(t, f.destination, "worktree", "add", "--quiet", "-b", "missing-model", worktree, latest)
		status, err := HandoffStatusFor(ctx, worktree)
		check(t, err)
		if status.Status != "blocked" || !strings.Contains(strings.Join(status.Warnings, " "), "no application model") {
			t.Fatal(status)
		}
	})
}

func TestHandoffConcurrentClaims(t *testing.T) {
	f, first := prepareHandoffFixture(t)
	second := filepath.Join(f.root, "second-worktree")
	g(t, f.destination, "worktree", "add", "--quiet", "-b", "second", second, f.base)
	a, err := HandoffStatusFor(ctx, first)
	check(t, err)
	b, err := HandoffStatusFor(ctx, second)
	check(t, err)
	results := make(chan error, 2)
	go func() { _, err := ApplyHandoff(ctx, first, a.RequestID, a.ReviewSHA256); results <- err }()
	go func() { _, err := ApplyHandoff(ctx, second, b.RequestID, b.ReviewSHA256); results <- err }()
	success := 0
	for range 2 {
		if <-results == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("expected one successful claim, got %d", success)
	}
}

func TestDesktopRejectsWorkspaceExecutable(t *testing.T) {
	f := newFixture(t, false)
	file := filepath.Join(f.source, "github")
	write(t, f.source, "github", "not executable")
	t.Setenv("RADPACK_GITHUB_COMMAND", file)
	fails(t, launchCopilot(ctx, f.source), "inside downloaded")
	t.Setenv("RADPACK_GITHUB_COMMAND", "relative/path")
	fails(t, launchCopilot(ctx, f.source), "absolute")
}
