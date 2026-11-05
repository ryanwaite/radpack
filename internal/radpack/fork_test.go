package radpack

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const personalForkURL = "https://github.com/personal-user/app.git"

type fakeForkGitHub struct {
	user        githubPerson
	repository  *githubFork
	created     *githubFork
	calls       []string
	postCount   int
	postStatus  int
	postErr     error
	readErr     error
	hideCreated bool
	before      func(method, endpoint string)
}

func publicFork(source, destination string) *githubFork {
	f := &githubFork{ID: 99, FullName: repoSlug(destination), Fork: true,
		Owner: githubPerson{Login: "personal-user", ID: 42, Type: "User"}}
	f.Permissions.Admin = true
	f.Parent = &struct {
		FullName string `json:"full_name"`
		ID       int64  `json:"id"`
	}{repoSlug(source), 22}
	f.Source = &struct {
		FullName string `json:"full_name"`
		ID       int64  `json:"id"`
	}{repoSlug(source), 22}
	return f
}
func forkAPI(t *testing.T) *fakeForkGitHub {
	t.Helper()
	return &fakeForkGitHub{user: githubPerson{"personal-user", 42, "User"}, postStatus: 202}
}
func (f *fakeForkGitHub) call(ctx context.Context, method, endpoint string, body any) (int, []byte, error) {
	if f.before != nil {
		f.before(method, endpoint)
	}
	f.calls = append(f.calls, method+" "+endpoint)
	if f.readErr != nil {
		return 0, nil, f.readErr
	}
	switch {
	case method == "GET" && endpoint == "user":
		b, _ := json.Marshal(f.user)
		return 200, b, nil
	case method == "GET" && endpoint == "repos/personal-user/app":
		if f.repository == nil {
			return 404, []byte(`{"message":"Not Found"}`), nil
		}
		b, _ := json.Marshal(f.repository)
		return 200, b, nil
	case method == "POST" && endpoint == "repos/radpack-fixture/app/forks":
		f.postCount++
		options, ok := body.(map[string]any)
		if !ok || options["name"] != "app" || options["default_branch_only"] != false || len(options) != 2 {
			return 0, nil, fmt.Errorf("unexpected fork creation parameters")
		}
		if f.postErr != nil {
			return 0, nil, f.postErr
		}
		if f.postStatus == 202 {
			f.created = publicFork(repo, personalForkURL)
			if !f.hideCreated {
				f.repository = f.created
			}
		}
		return f.postStatus, []byte(`{}`), nil
	default:
		return 0, nil, fmt.Errorf("unexpected GitHub request %s %s", method, endpoint)
	}
}
func mapForkGit(t *testing.T, f *fixture) {
	t.Helper()
	g(t, f.root, "config", "--global", "--add", "url."+filepath.ToSlash(f.source)+".insteadOf", personalForkURL)
}
func previewFork(t *testing.T, root string, api *fakeForkGitHub) ForkReport {
	t.Helper()
	var out strings.Builder
	check(t, forkCommand(ctx, map[string][]string{"directory": {root}, "json": {"true"}}, &out, nil, api.call))
	var report ForkReport
	check(t, json.Unmarshal([]byte(out.String()), &report))
	if report.Status != "awaiting-approval" || report.Review == nil || !hashPattern.MatchString(report.ReviewSHA256) {
		t.Fatal(out.String())
	}
	return report
}
func approvedFork(t *testing.T, root, approval string, api *fakeForkGitHub) (ForkReport, error) {
	t.Helper()
	var out strings.Builder
	err := forkCommand(ctx, map[string][]string{"directory": {root}, "approve": {approval}, "json": {"true"}}, &out, nil, api.call)
	var report ForkReport
	if parseErr := json.Unmarshal([]byte(out.String()), &report); parseErr != nil {
		t.Fatalf("report: %s: %v", out.String(), parseErr)
	}
	return report, err
}

func TestForkCreateAndSourceProvenance(t *testing.T) {
	f := newAuthoringFixture(t)
	mapForkGit(t, f)
	prepareURL(t, f)
	radify(t, f.destination)
	write(t, f.destination, "app.js", "local changes not pushed\n")
	originalHead := g(t, f.destination, "rev-parse", "HEAD")
	statusBefore := g(t, f.destination, "status", "--porcelain")
	api := forkAPI(t)
	plan := previewFork(t, f.destination, api)
	if api.postCount != 0 {
		t.Fatal("preview mutated GitHub")
	}
	if plan.Review.Application.Repository != repo || plan.Review.Application.Commit != f.base {
		t.Fatal(plan)
	}
	binding, err := readDeploymentBinding(ctx, f.destination)
	check(t, err)
	if binding != nil {
		t.Fatal("preview wrote binding")
	}
	applied, err := approvedFork(t, f.destination, plan.ReviewSHA256, api)
	check(t, err)
	if applied.Status != "ready" || api.postCount != 1 {
		t.Fatal(applied)
	}
	if g(t, f.destination, "config", "--get", "remote.origin.url") != personalForkURL ||
		g(t, f.destination, "config", "--get", "remote.upstream.url") != repo {
		t.Fatal("wrong remotes")
	}
	if g(t, f.destination, "rev-parse", "HEAD") != originalHead || g(t, f.destination, "status", "--porcelain") != statusBefore {
		t.Fatal("fork setup changed application files or branch")
	}
	author, err := loadAuthoring(ctx, f.destination)
	check(t, err)
	if author.record.Upstream.Repository != repo {
		t.Fatal("fork became source")
	}
	source, err := prepareCreateSource(ctx, CreateOptions{Source: f.destination}, nil)
	check(t, err)
	defer source.close()
	if source.Options.Upstream != repo || source.Options.Base != f.base {
		t.Fatal(source.Options)
	}
	packPlan, err := PlanCreate(ctx, source.Options)
	check(t, err)
	if packPlan.Manifest.Upstream.Repository != repo {
		t.Fatal("private deployment state leaked into manifest")
	}
	for _, file := range packPlan.Manifest.Files {
		if strings.Contains(file.Path, "deployment") {
			t.Fatal("binding exported")
		}
	}
	if strings.Contains(strings.Join(api.calls, "\n"), "GET repos/radpack-fixture/app") {
		t.Fatal("setup depended on upstream admin API")
	}
	for _, call := range api.calls {
		if strings.Contains(call, "environments") || strings.Contains(call, "actions") {
			t.Fatal("deployment side effect")
		}
	}
	var out strings.Builder
	check(t, forkCommand(ctx, map[string][]string{"directory": {f.destination}, "json": {"true"}}, &out, nil, api.call))
	if api.postCount != 1 {
		t.Fatal("ready retry created another fork")
	}
}

func TestForkReuseAndConflicts(t *testing.T) {
	for _, mode := range []string{"valid", "unrelated", "not-fork", "not-admin", "private", "different-owner", "archived", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, false)
			mapForkGit(t, f)
			api := forkAPI(t)
			api.repository = publicFork(repo, personalForkURL)
			switch mode {
			case "unrelated":
				api.repository.Parent.FullName = "other/project"
				api.repository.Source.FullName = "other/project"
			case "not-fork":
				api.repository.Fork = false
			case "not-admin":
				api.repository.Permissions.Admin = false
			case "private":
				api.repository.Private = true
			case "different-owner":
				api.repository.Owner.ID = 100
			case "archived":
				api.repository.Archived = true
			case "disabled":
				api.repository.Disabled = true
			}
			var out strings.Builder
			err := forkCommand(ctx, map[string][]string{"directory": {f.source}, "json": {"true"}}, &out, nil, api.call)
			if mode != "valid" {
				if err == nil {
					t.Fatal("invalid destination allowed")
				}
				return
			}
			check(t, err)
			var report ForkReport
			check(t, json.Unmarshal([]byte(out.String()), &report))
			if report.Review.Action != "reuse-personal-fork" {
				t.Fatal(report)
			}
			_, err = approvedFork(t, f.source, report.ReviewSHA256, api)
			check(t, err)
			if api.postCount != 0 {
				t.Fatal("reused fork was recreated")
			}
		})
	}
}

func TestForkDirtyWorkingTreesAndInstallReceipts(t *testing.T) {
	f := newFixture(t, false)
	mapForkGit(t, f)
	f.publish(t)
	f.install(t, false)
	receiptBefore := read(t, f.destination, receiptPath)
	api := forkAPI(t)
	api.repository = publicFork(repo, personalForkURL)
	review := previewFork(t, f.destination, api)
	_, err := approvedFork(t, f.destination, review.ReviewSHA256, api)
	check(t, err)
	_, err = VerifyInstallation(ctx, "", f.destination)
	check(t, err)
	if read(t, f.destination, receiptPath) != receiptBefore {
		t.Fatal("portable receipt was rewritten")
	}
	g(t, f.destination, "remote", "set-url", "origin", "https://github.com/unrelated/repo.git")
	_, err = VerifyInstallation(ctx, "", f.destination)
	fails(t, err, "remotes differ")
}

func TestForkHandoffUsesDeploymentOrigin(t *testing.T) {
	f, worktree := prepareHandoffFixture(t)
	mapForkGit(t, f)
	before, err := HandoffStatusFor(ctx, worktree)
	check(t, err)
	api := forkAPI(t)
	api.repository = publicFork(repo, personalForkURL)
	review := previewFork(t, f.destination, api)
	_, err = approvedFork(t, f.destination, review.ReviewSHA256, api)
	check(t, err)
	status, err := HandoffStatusFor(ctx, worktree)
	check(t, err)
	if status.Repository != repo || status.DeploymentRepository != personalForkURL || status.ReviewSHA256 == before.ReviewSHA256 {
		t.Fatal(status)
	}
	_, err = ApplyHandoff(ctx, worktree, before.RequestID, before.ReviewSHA256)
	fails(t, err, "approval")
	_, err = ApplyHandoff(ctx, worktree, status.RequestID, status.ReviewSHA256)
	check(t, err)
	_, err = VerifyInstallation(ctx, "", worktree)
	check(t, err)
}

func TestForkPendingRetryDoesNotRepeatMutation(t *testing.T) {
	for _, mode := range []string{"asynchronous", "unknown-outcome", "known-denied", "not-fetchable"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, false)
			mapForkGit(t, f)
			api := forkAPI(t)
			switch mode {
			case "asynchronous":
				api.hideCreated = true
			case "unknown-outcome":
				api.postErr = errors.New("connection lost")
			case "known-denied":
				api.postStatus = 403
			case "not-fetchable":
				// Rewrite only the fork URL to a separate repository that cannot
				// supply the pinned source commit.
				g(t, f.root, "config", "--global", "--unset-all", "url."+filepath.ToSlash(f.source)+".insteadOf")
				g(t, f.root, "config", "--global", "--add", "url."+filepath.ToSlash(f.source)+".insteadOf", repo)
				empty := filepath.Join(f.root, "empty")
				check(t, os.Mkdir(empty, 0755))
				g(t, empty, "init", "--quiet", "--bare")
				g(t, f.root, "config", "--global", "url."+filepath.ToSlash(empty)+".insteadOf", personalForkURL)
			}
			review := previewFork(t, f.source, api)
			_, err := approvedFork(t, f.source, review.ReviewSHA256, api)
			if err == nil {
				t.Fatal("expected incomplete fork setup")
			}
			if g(t, f.source, "config", "--get", "remote.origin.url") != repo {
				t.Fatal("changed origin before verifying fork")
			}
			record, err := readDeploymentBinding(ctx, f.source)
			check(t, err)
			if mode == "known-denied" {
				if record != nil {
					t.Fatal("definitively rejected creation left a blocking request")
				}
			} else if record == nil || record.Status != "pending" {
				t.Fatal(record)
			}
			if mode == "asynchronous" || mode == "unknown-outcome" {
				_, err = approvedFork(t, f.source, review.ReviewSHA256, api)
				fails(t, err, "outcome is not confirmed")
				if api.postCount != 1 {
					t.Fatal("duplicate fork request")
				}
				api.repository = publicFork(repo, personalForkURL)
				api.postErr = nil
			}
			if mode == "known-denied" {
				api.postStatus = 202
			}
			if mode == "not-fetchable" {
				api.repository = publicFork(repo, personalForkURL)
				// Restore source availability without replacing the fork ID.
				g(t, f.root, "config", "--global", "--unset-all", "url."+filepath.ToSlash(filepath.Join(f.root, "empty"))+".insteadOf")
				mapForkGit(t, f)
			}
			_, err = approvedFork(t, f.source, review.ReviewSHA256, api)
			check(t, err)
			record, err = readDeploymentBinding(ctx, f.source)
			check(t, err)
			if record.Status != "ready" {
				t.Fatal(record)
			}
		})
	}
}

func TestForkApprovalAndAccountDrift(t *testing.T) {
	for _, mode := range []string{"approval", "account", "repository", "upstream", "worktree"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, false)
			mapForkGit(t, f)
			api := forkAPI(t)
			review := previewFork(t, f.source, api)
			switch mode {
			case "approval":
				review.ReviewSHA256 = strings.Repeat("0", 64)
			case "account":
				api.user.ID++
			case "repository":
				api.repository = publicFork(repo, personalForkURL)
			case "upstream":
				g(t, f.source, "remote", "add", "upstream", "https://github.com/different/app.git")
			case "worktree":
				g(t, f.source, "worktree", "add", "--quiet", "-b", "other", filepath.Join(f.root, "other"), f.base)
			}
			_, err := approvedFork(t, f.source, review.ReviewSHA256, api)
			if err == nil || api.postCount != 0 {
				t.Fatal("stale authorization mutated GitHub")
			}
		})
	}
}

func TestForkCancelAndDryRun(t *testing.T) {
	f := newFixture(t, false)
	api := forkAPI(t)
	for _, answer := range []string{"", "\n", "no\n"} {
		var out strings.Builder
		ui := &createUI{bufio.NewReader(strings.NewReader(answer)), &out}
		check(t, forkCommand(ctx, map[string][]string{"directory": {f.source}}, &out, ui, api.call))
		if !strings.Contains(out.String(), "Cancelled") {
			t.Fatal(out.String())
		}
	}
	var out strings.Builder
	check(t, forkCommand(ctx, map[string][]string{"directory": {f.source}, "dry-run": {"true"}}, &out,
		&createUI{bufio.NewReader(panicReader{}), &out}, api.call))
	if api.postCount != 0 {
		t.Fatal("preview created fork")
	}
	if _, err := os.Stat(filepath.Join(f.source, ".git", deploymentBindingFile)); !os.IsNotExist(err) {
		t.Fatal("cancel/preview wrote state")
	}
}

func TestForkRemoteConfigurationGuards(t *testing.T) {
	for _, mode := range []string{"pushurl", "multiple-url", "upstream", "mirror"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, false)
			api := forkAPI(t)
			switch mode {
			case "pushurl":
				g(t, f.source, "config", "remote.origin.pushurl", "https://github.com/elsewhere/app.git")
			case "multiple-url":
				g(t, f.source, "config", "--add", "remote.origin.url", repo)
			case "upstream":
				g(t, f.source, "remote", "add", "upstream", "https://github.com/different/app.git")
			case "mirror":
				g(t, f.source, "config", "remote.origin.mirror", "true")
			}
			var out strings.Builder
			if forkCommand(ctx, map[string][]string{"directory": {f.source}, "json": {"true"}}, &out, nil, api.call) == nil {
				t.Fatal("unsafe remote configuration accepted")
			}
			if api.postCount != 0 {
				t.Fatal("fork created before local checks")
			}
		})
	}
}

func TestForkJournalRecoveryAndReplacement(t *testing.T) {
	f := newFixture(t, false)
	mapForkGit(t, f)
	api := forkAPI(t)
	api.hideCreated = true
	review := previewFork(t, f.source, api)
	_, err := approvedFork(t, f.source, review.ReviewSHA256, api)
	if err == nil {
		t.Fatal("expected pending")
	}
	api.repository = api.created
	// Simulate a crash after upstream was added and origin changed, but before
	// the final ready checkpoint. Exact approved remotes may be resumed.
	g(t, f.source, "remote", "add", "upstream", repo)
	g(t, f.source, "remote", "set-url", "origin", personalForkURL)
	_, err = approvedFork(t, f.source, review.ReviewSHA256, api)
	check(t, err)
	binding, err := readDeploymentBinding(ctx, f.source)
	check(t, err)
	if binding.RepositoryID != 99 || binding.Status != "ready" {
		t.Fatal(binding)
	}
	api.repository.ID = 100
	var out strings.Builder
	err = forkCommand(ctx, map[string][]string{"directory": {f.source}, "json": {"true"}}, &out, nil, api.call)
	fails(t, err, "repository ID changed")
}

func TestForkGitHubFailuresAndParsing(t *testing.T) {
	for _, text := range []string{
		"HTTP/2.0 200 OK\r\nX-Header: value\r\n\r\n{\"login\":\"user\"}",
		"HTTP/1.1 404 Not Found\nX-Header: value\n\n{\"message\":\"Not Found\"}",
	} {
		code, body, err := parseGitHubResponse([]byte(text))
		check(t, err)
		if code != 200 && code != 404 {
			t.Fatal(code)
		}
		if !json.Valid(body) {
			t.Fatal(string(body))
		}
	}
	for _, text := range []string{"not HTTP", "HTTP/2 nonsense\n\n{}", "HTTP/2 999\n\n{}"} {
		if _, _, err := parseGitHubResponse([]byte(text)); err == nil {
			t.Fatal("invalid response accepted")
		}
	}
	f := newFixture(t, false)
	api := forkAPI(t)
	api.readErr = githubFailure(403)
	var out strings.Builder
	err := forkCommand(ctx, map[string][]string{"directory": {f.source}, "json": {"true"}}, &out, nil, api.call)
	fails(t, err, "SAML")
	if api.postCount != 0 {
		t.Fatal("read failure mutated")
	}
	api.readErr = nil
	api.user.Type = "Bot"
	err = forkCommand(ctx, map[string][]string{"directory": {f.source}, "json": {"true"}}, &out, nil, api.call)
	fails(t, err, "personal user")
}

func TestUnrecordedRemoteChangeStillFailsVerification(t *testing.T) {
	f := newFixture(t, false)
	f.publish(t)
	f.install(t, false)
	g(t, f.destination, "remote", "set-url", "origin", personalForkURL)
	g(t, f.destination, "remote", "add", "upstream", repo)
	_, err := VerifyInstallation(ctx, "", f.destination)
	fails(t, err, "origin differs")
}

func TestForkReadinessDoesNotPublishEnvironmentOrSource(t *testing.T) {
	f := newFixture(t, false)
	mapForkGit(t, f)
	api := forkAPI(t)
	api.repository = publicFork(repo, personalForkURL)
	localBefore := g(t, f.source, "rev-parse", "HEAD")
	remotesBefore, err := readDeploymentRemotes(ctx, f.source)
	check(t, err)
	review := previewFork(t, f.source, api)
	if !reflect.DeepEqual(review.Review.OriginalRemotes, remotesBefore) {
		t.Fatal(review)
	}
	_, err = approvedFork(t, f.source, review.ReviewSHA256, api)
	check(t, err)
	if g(t, f.source, "rev-parse", "HEAD") != localBefore {
		t.Fatal("changed source commit")
	}
	for _, call := range api.calls {
		if strings.HasPrefix(call, "POST") {
			t.Fatal("existing fork must have no API writes", call)
		}
	}
}

func TestForkCommandUsesSelectedGitHubCLI(t *testing.T) {
	// Execute only the Go test helper masquerading as gh, never a user's login.
	dir := t.TempDir()
	exe, err := os.Executable()
	check(t, err)
	content, err := os.ReadFile(exe)
	check(t, err)
	name := "gh"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	check(t, os.WriteFile(filepath.Join(dir, name), content, 0755))
	t.Setenv("PATH", dir)
	t.Setenv("RADPACK_GITHUB_API_TEST_HELPER", "user")
	person, err := readGitHubUser(ctx, callGitHub)
	check(t, err)
	if person.Login != "personal-user" || person.ID != 42 {
		t.Fatal(person)
	}
	t.Setenv("RADPACK_GITHUB_API_TEST_HELPER", "denied")
	_, err = readGitHubUser(ctx, callGitHub)
	fails(t, err, "SAML")
	if strings.Contains(err.Error(), "sensitive-content") || strings.Contains(err.Error(), "stderr-not-for-output") {
		t.Fatal("API output leaked")
	}
	t.Setenv("RADPACK_GITHUB_API_TEST_HELPER", "invalid")
	_, err = readGitHubUser(ctx, callGitHub)
	if err == nil || strings.Contains(err.Error(), "unreadable-sensitive-content") {
		t.Fatal(err)
	}
}

func TestForkFromLinkedWorktree(t *testing.T) {
	f := newFixture(t, false)
	mapForkGit(t, f)
	worktree := filepath.Join(f.root, "linked")
	g(t, f.source, "worktree", "add", "--quiet", "-b", "worktree", worktree, f.base)
	api := forkAPI(t)
	api.repository = publicFork(repo, personalForkURL)
	review := previewFork(t, worktree, api)
	if len(review.Review.Worktrees) != 2 {
		t.Fatal("missing shared checkout warning")
	}
	_, err := approvedFork(t, worktree, review.ReviewSHA256, api)
	check(t, err)
	for _, directory := range []string{f.source, worktree} {
		binding, err := readDeploymentBinding(ctx, directory)
		check(t, err)
		if binding == nil || binding.Status != "ready" {
			t.Fatal(binding)
		}
		check(t, verifyApplicationOrigin(ctx, directory, repo))
	}
}

func TestForkDoesNotBypassExistingLockOrSymlink(t *testing.T) {
	f := newFixture(t, false)
	mapForkGit(t, f)
	api := forkAPI(t)
	review := previewFork(t, f.source, api)
	common, err := commonDirectory(ctx, f.source)
	check(t, err)
	write(t, common, "radpack-fork.lock", "interrupted")
	_, err = approvedFork(t, f.source, review.ReviewSHA256, api)
	fails(t, err, "already active")
	if api.postCount != 0 {
		t.Fatal("lock bypassed")
	}
	check(t, os.Remove(filepath.Join(common, "radpack-fork.lock")))
	if runtime.GOOS == "windows" {
		return
	}
	target := filepath.Join(f.root, "do-not-touch")
	write(t, f.root, "do-not-touch", "original")
	check(t, os.Symlink(target, filepath.Join(common, deploymentBindingFile)))
	_, err = approvedFork(t, f.source, review.ReviewSHA256, api)
	fails(t, err, "symbolic")
	if read(t, f.root, "do-not-touch") != "original" {
		t.Fatal("symlink target changed")
	}
}

func TestForkExplicitRejectionAllowsNewReview(t *testing.T) {
	f := newFixture(t, false)
	mapForkGit(t, f)
	api := forkAPI(t)
	api.postStatus = 422
	review := previewFork(t, f.source, api)
	_, err := approvedFork(t, f.source, review.ReviewSHA256, api)
	fails(t, err, "another name")
	binding, err := readDeploymentBinding(ctx, f.source)
	check(t, err)
	if binding != nil {
		t.Fatal("known rejection left unfinished state")
	}
	if g(t, f.source, "config", "--get", "remote.origin.url") != repo {
		t.Fatal("refusal changed origin")
	}
	api.repository = publicFork(repo, personalForkURL)
	reused := previewFork(t, f.source, api)
	if reused.Review.Action != "reuse-personal-fork" {
		t.Fatal(reused)
	}
	_, err = approvedFork(t, f.source, reused.ReviewSHA256, api)
	check(t, err)
	if api.postCount != 1 {
		t.Fatal("reuse issued another creation request")
	}
}
