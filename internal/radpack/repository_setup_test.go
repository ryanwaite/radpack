package radpack

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const independentURL = "https://github.com/personal-user/app-radpack.git"

type repositoryAPI struct {
	user          githubPerson
	repository    *githubFork
	calls         []string
	creates       int
	disables      int
	enabled       bool
	createStatus  int
	createErr     error
	disableStatus int
	before        func(string, string)
}

func (api *repositoryAPI) call(ctx context.Context, method, endpoint string, body any) (int, []byte, error) {
	if api.before != nil {
		api.before(method, endpoint)
	}
	api.calls = append(api.calls, method+" "+endpoint)
	switch {
	case method == "GET" && endpoint == "user":
		data, _ := json.Marshal(api.user)
		return 200, data, nil
	case method == "GET" && endpoint == "repos/personal-user/app-radpack":
		if api.repository == nil {
			return 404, nil, nil
		}
		data, _ := json.Marshal(api.repository)
		return 200, data, nil
	case method == "POST" && endpoint == "user/repos":
		api.creates++
		options, ok := body.(map[string]any)
		if !ok || len(options) != 3 || options["name"] != "app-radpack" || options["auto_init"] != false {
			return 0, nil, fmt.Errorf("unexpected repository creation parameters")
		}
		if api.createErr != nil {
			return 0, nil, api.createErr
		}
		if api.createStatus != 201 {
			return api.createStatus, nil, nil
		}
		api.repository = publicFork(repo, independentURL)
		api.repository.Fork = false
		api.repository.Private = options["private"] == true
		data, _ := json.Marshal(api.repository)
		return 201, data, nil
	case endpoint == "repos/personal-user/app-radpack/actions/permissions":
		if method == "PUT" {
			api.disables++
			options, ok := body.(map[string]any)
			if !ok || len(options) != 1 || options["enabled"] != false {
				return 0, nil, fmt.Errorf("unexpected Actions policy mutation")
			}
			if api.disableStatus != 204 {
				return api.disableStatus, nil, nil
			}
			api.enabled = false
			return 204, nil, nil
		}
		if method == "GET" {
			data, _ := json.Marshal(map[string]bool{"enabled": api.enabled})
			return 200, data, nil
		}
	}
	return 0, nil, fmt.Errorf("unexpected API call: %s %s", method, endpoint)
}

func independentFixture(t *testing.T) (*fixture, *repositoryAPI, string) {
	t.Helper()
	f := newAuthoringFixture(t)
	g(t, f.root, "config", "--global", "user.name", "Radpack tests")
	g(t, f.root, "config", "--global", "user.email", "tests@example.invalid")
	remote := filepath.Join(f.root, "independent.git")
	check(t, os.Mkdir(remote, 0700))
	g(t, remote, "init", "--bare", "--quiet", "--template=")
	g(t, f.root, "config", "--global", "--add", "url."+filepath.ToSlash(remote)+".insteadOf", independentURL)
	return f, &repositoryAPI{user: githubPerson{"personal-user", 42, "User"}, enabled: true, createStatus: 201, disableStatus: 204}, remote
}

func approveRepository(t *testing.T, f *fixture, api *repositoryAPI, sha string) (AuthoringReport, error) {
	t.Helper()
	return resumeDeploymentReview(t, f, map[string][]string{"repository-approve": {sha}}, api.call)
}

func TestIndependentDefaultSnapshotAndPackProvenance(t *testing.T) {
	f, api, remote := independentFixture(t)
	// Include original license and workflow bytes, but not any new author files.
	write(t, f.source, "LICENSE", "Original upstream license and attribution\n")
	write(t, f.source, ".github/workflows/sample.yml", "on: push\njobs: {}\n")
	g(t, f.source, "add", "LICENSE", ".github/workflows/sample.yml")
	g(t, f.source, "commit", "--quiet", "-m", "Add notices and workflow")
	f.base = g(t, f.source, "rev-parse", "HEAD")
	first, err := startDeploymentReview(t, f, nil, api.call)
	check(t, err)
	review := first.RepositorySetup.PublicationReview
	if first.Status != "awaiting-repository-approval" || review == nil || !review.Private ||
		review.Repository != independentURL || api.creates != 0 || api.disables != 0 {
		t.Fatal(first)
	}
	for _, call := range api.calls {
		if strings.Contains(call, "radpack-fixture") || strings.HasSuffix(call, "/forks") {
			t.Fatal("independent path contacted upstream API", call)
		}
	}
	write(t, f.destination, "local-only.txt", "never publish this\n")
	write(t, f.destination, "app.js", "local staged edit\n")
	g(t, f.destination, "add", "app.js")
	status := g(t, f.destination, "status", "--porcelain")
	result, err := approveRepository(t, f, api, first.RepositorySetup.ReviewSHA256)
	check(t, err)
	if result.Status != "awaiting-radification" || result.RepositorySetup.Status != "ready" ||
		result.RepositorySetup.SourcePublication != "confirmed" || api.creates != 1 || api.enabled {
		t.Fatal(result)
	}
	binding, err := readDeploymentBinding(ctx, f.destination)
	check(t, err)
	if binding.Kind != "independent" || !binding.Private || binding.Application.Commit != f.base {
		t.Fatal(binding)
	}
	check(t, verifySnapshotCommit(ctx, remote, binding))
	if g(t, remote, "show", binding.SnapshotCommit+":app.js") != "upstream" {
		t.Fatal("published the local index instead of the pinned tree")
	}
	if g(t, remote, "show", binding.SnapshotCommit+":LICENSE") != "Original upstream license and attribution" {
		t.Fatal("license missing")
	}
	if g(t, remote, "rev-list", "--count", binding.SnapshotCommit) != "1" {
		t.Fatal("published upstream history")
	}
	if !strings.Contains(g(t, remote, "show", "-s", "--format=%B", binding.SnapshotCommit), f.base) {
		t.Fatal("missing upstream attribution")
	}
	if g(t, f.destination, "status", "--porcelain") != status ||
		g(t, f.destination, "rev-parse", "radpack") != f.base ||
		g(t, f.destination, "symbolic-ref", "--short", "HEAD") != "radpack-authoring" {
		t.Fatal("local work or original branch was lost")
	}
	refs, err := repositoryRefs(ctx, independentURL)
	check(t, err)
	if len(refs) != 1 || refs[authoringBranch] != binding.SnapshotCommit {
		t.Fatal(refs)
	}
	// Ready resumes must not disable Actions again after a user enables them.
	api.enabled = true
	_, err = resumeDeploymentReview(t, f, nil, api.call)
	check(t, err)
	if api.creates != 1 || api.disables != 1 || !api.enabled {
		t.Fatal("ready resume repeated remote mutation")
	}
	radify(t, f.destination)
	g(t, f.destination, "add", ".radius")
	g(t, f.destination, "commit", "--quiet", "-m", "Radify snapshot")
	g(t, f.destination, "push", "--quiet")
	var out strings.Builder
	check(t, authoringWorkflowWithAPI(ctx, "", map[string][]string{"resume": {f.destination}, "json": {"true"}}, &out, nil, true, api.call))
	var packReview CreateReport
	check(t, json.Unmarshal([]byte(out.String()), &packReview))
	if packReview.Review.Manifest.Upstream.Repository != repo || packReview.Review.Manifest.Upstream.Commit != f.base {
		t.Fatal("pack lost original application identity")
	}
	out.Reset()
	check(t, authoringWorkflowWithAPI(ctx, "", map[string][]string{"resume": {f.destination}, "json": {"true"}, "approve": {packReview.ReviewSHA256}}, &out, nil, true, api.call))
	pack, err := LoadPack(f.pack)
	check(t, err)
	if pack.Manifest.Upstream.Commit != f.base {
		t.Fatal(pack.Manifest.Upstream)
	}
}

func TestIndependentPublicRequiresExplicitChoice(t *testing.T) {
	f, api, _ := independentFixture(t)
	first, err := startDeploymentReview(t, f, map[string][]string{"public": {"true"}}, api.call)
	check(t, err)
	if first.RepositorySetup.PublicationReview.Private {
		t.Fatal("public option not reviewed")
	}
	_, err = approveRepository(t, f, api, first.RepositorySetup.ReviewSHA256)
	check(t, err)
	if api.repository.Private {
		t.Fatal("explicit public choice ignored")
	}
}

func TestIndependentExistingRepositoryMustBeExplicitAndEmpty(t *testing.T) {
	for _, populated := range []bool{false, true} {
		t.Run(fmt.Sprint(populated), func(t *testing.T) {
			f, api, remote := independentFixture(t)
			api.repository = publicFork(repo, independentURL)
			api.repository.Fork = false
			api.repository.Private = true
			if populated {
				g(t, f.source, "push", "--quiet", remote, "HEAD:refs/heads/main")
			}
			_, err := startDeploymentReview(t, f, nil, api.call)
			fails(t, err, "explicitly select")
			report, err := resumeDeploymentReview(t, f, map[string][]string{"repository": {independentURL}}, api.call)
			if populated {
				fails(t, err, "not empty")
				return
			}
			check(t, err)
			if report.RepositorySetup.PublicationReview.Action != "populate-explicit-empty-repository" {
				t.Fatal(report)
			}
			_, err = approveRepository(t, f, api, report.RepositorySetup.ReviewSHA256)
			check(t, err)
			if api.creates != 0 || api.enabled {
				t.Fatal("existing repo recreated or Actions not disabled")
			}
		})
	}
}

func TestIndependentConsentAndStatusAreReadOnly(t *testing.T) {
	for _, input := range []string{"", "\nq\n", "n\nq\n"} {
		t.Run(fmt.Sprintf("%q", input), func(t *testing.T) {
			f, api, remote := independentFixture(t)
			var out strings.Builder
			check(t, authoringWorkflowWithAPI(ctx, repo, map[string][]string{"directory": {f.destination}}, &out,
				&createUI{bufio.NewReader(strings.NewReader(input)), &out}, true, api.call))
			if api.creates != 0 || api.disables != 0 || g(t, remote, "for-each-ref") != "" {
				t.Fatal("unapproved publication")
			}
			if !strings.Contains(out.String(), "disable Actions, and publish this source snapshot? [y/N]") {
				t.Fatal(out.String())
			}
			_, err := resumeDeploymentReview(t, f, map[string][]string{"status": {"true"}}, nil)
			check(t, err)
		})
	}
}

func TestIndependentSourceAndVisibilityApprovalDrift(t *testing.T) {
	f, api, _ := independentFixture(t)
	first, err := startDeploymentReview(t, f, nil, api.call)
	check(t, err)
	_, err = resumeDeploymentReview(t, f, map[string][]string{"public": {"true"}, "repository-approve": {first.RepositorySetup.ReviewSHA256}}, api.call)
	fails(t, err, "approval does not match")
	if api.creates != 0 {
		t.Fatal("changed visibility published without approval")
	}
	restored, err := resumeDeploymentReview(t, f, map[string][]string{"private": {"true"}}, api.call)
	check(t, err)
	if !restored.RepositorySetup.PublicationReview.Private || restored.RepositorySetup.ReviewSHA256 != first.RepositorySetup.ReviewSHA256 {
		t.Fatal("could not restore private choice")
	}
}

func TestIndependentDisablesActionsBeforePushAndRetries(t *testing.T) {
	f, api, remote := independentFixture(t)
	api.disableStatus = 403
	first, err := startDeploymentReview(t, f, nil, api.call)
	check(t, err)
	_, err = approveRepository(t, f, api, first.RepositorySetup.ReviewSHA256)
	fails(t, err, "disable destination Actions")
	if g(t, remote, "for-each-ref") != "" || api.creates != 1 {
		t.Fatal("pushed before Actions disabled")
	}
	_, err = resumeDeploymentReview(t, f, map[string][]string{"no-fork": {"true"}}, api.call)
	fails(t, err, "cannot undo or abandon")
	api.disableStatus = 204
	result, err := approveRepository(t, f, api, first.RepositorySetup.ReviewSHA256)
	check(t, err)
	if result.RepositorySetup.Status != "ready" || api.creates != 1 {
		t.Fatal("retry duplicated repository", result)
	}
}

func TestIndependentUnknownCreateDoesNotClaimMatchingRepository(t *testing.T) {
	f, api, remote := independentFixture(t)
	api.createErr = errors.New("response lost")
	first, err := startDeploymentReview(t, f, nil, api.call)
	check(t, err)
	_, err = approveRepository(t, f, api, first.RepositorySetup.ReviewSHA256)
	fails(t, err, "outcome is unknown")
	api.createErr = nil
	api.repository = publicFork(repo, independentURL)
	api.repository.Fork = false
	api.repository.Private = true
	_, err = approveRepository(t, f, api, first.RepositorySetup.ReviewSHA256)
	fails(t, err, "without a confirmed creation identity")
	if api.creates != 1 || api.disables != 0 || g(t, remote, "for-each-ref") != "" {
		t.Fatal("uncertain creation was repeated or claimed")
	}
}

func TestIndependentRejectsConcurrentDestinationRefs(t *testing.T) {
	f, api, remote := independentFixture(t)
	first, err := startDeploymentReview(t, f, nil, api.call)
	check(t, err)
	api.before = func(method, endpoint string) {
		if method == "PUT" {
			g(t, f.source, "push", "--quiet", remote, "HEAD:refs/heads/main")
		}
	}
	_, err = approveRepository(t, f, api, first.RepositorySetup.ReviewSHA256)
	fails(t, err, "no longer confirmed empty")
	if g(t, remote, "rev-parse", "refs/heads/main") != f.base {
		t.Fatal("concurrent ref was overwritten")
	}
}

func TestIndependentCompletedPushResumesWithoutRepublishing(t *testing.T) {
	f, api, remote := independentFixture(t)
	first, err := startDeploymentReview(t, f, nil, api.call)
	check(t, err)
	api.before = func(method, endpoint string) {
		if method == "GET" && strings.HasSuffix(endpoint, "/actions/permissions") && g(t, remote, "for-each-ref") != "" {
			api.enabled = true
		}
	}
	result, err := approveRepository(t, f, api, first.RepositorySetup.ReviewSHA256)
	fails(t, err, "Actions must be confirmed disabled")
	if result.RepositorySetup.SourcePublication != "unconfirmed" {
		t.Fatal(result)
	}
	binding, err := readDeploymentBinding(ctx, f.destination)
	check(t, err)
	published := g(t, remote, "rev-parse", authoringBranch)
	if binding.Status != "pending" || published != binding.SnapshotCommit {
		t.Fatal(binding)
	}
	api.before = nil
	api.enabled = false
	_, err = approveRepository(t, f, api, first.RepositorySetup.ReviewSHA256)
	check(t, err)
	if api.creates != 1 || api.disables != 1 || g(t, remote, "rev-parse", authoringBranch) != published {
		t.Fatal("retry repeated creation or publication")
	}
}

func TestIndependentSigningFailureDoesNotCreateRepository(t *testing.T) {
	f, api, _ := independentFixture(t)
	first, err := startDeploymentReview(t, f, nil, api.call)
	check(t, err)
	g(t, f.root, "config", "--global", "commit.gpgsign", "true")
	g(t, f.root, "config", "--global", "gpg.program", filepath.Join(f.root, "missing-gpg"))
	_, err = approveRepository(t, f, api, first.RepositorySetup.ReviewSHA256)
	fails(t, err, "configured signing is honored")
	if api.creates != 0 || api.disables != 0 {
		t.Fatal("signing failure fell back or mutated destination")
	}
}

func TestIndependentAccountAndIdentityDriftAreBlocked(t *testing.T) {
	for _, drift := range []string{"account", "repository-id", "visibility"} {
		t.Run(drift, func(t *testing.T) {
			f, api, remote := independentFixture(t)
			first, err := startDeploymentReview(t, f, nil, api.call)
			check(t, err)
			if drift == "account" {
				api.user.ID++
			} else {
				api.disableStatus = 403
				_, err = approveRepository(t, f, api, first.RepositorySetup.ReviewSHA256)
				fails(t, err, "disable destination Actions")
				api.disableStatus = 204
				if drift == "repository-id" {
					api.repository.ID++
				} else {
					api.repository.Private = false
				}
			}
			_, err = approveRepository(t, f, api, first.RepositorySetup.ReviewSHA256)
			if err == nil || g(t, remote, "for-each-ref") != "" {
				t.Fatal("published after drift", err)
			}
		})
	}
}

func TestIndependentExplicitCreateRejectionIsRecoverable(t *testing.T) {
	f, api, _ := independentFixture(t)
	first, err := startDeploymentReview(t, f, nil, api.call)
	check(t, err)
	api.createStatus = 403
	_, err = approveRepository(t, f, api, first.RepositorySetup.ReviewSHA256)
	fails(t, err, "POST user/repos")
	binding, err := readDeploymentBinding(ctx, f.destination)
	check(t, err)
	if binding != nil {
		t.Fatal("definite creation rejection retained a pending operation")
	}
	api.createStatus = 201
	_, err = approveRepository(t, f, api, first.RepositorySetup.ReviewSHA256)
	check(t, err)
}

func TestIndependentMigrationPreservesExistingAuthoringChoice(t *testing.T) {
	f, api, _ := independentFixture(t)
	prepareURL(t, f)
	first, err := resumeDeploymentReview(t, f, nil, nil)
	check(t, err)
	if first.RepositorySetup.Status != "skipped" {
		t.Fatal(first)
	}
	first, err = resumeDeploymentReview(t, f, map[string][]string{"independent": {"true"}}, api.call)
	check(t, err)
	if first.Status != "awaiting-repository-approval" || api.creates != 0 {
		t.Fatal(first)
	}
}

func TestIndependentSnapshotBlocksSecretsAndLFS(t *testing.T) {
	for _, content := range []string{
		"token: ghp_" + strings.Repeat("a", 30),
		"version https://git-lfs.github.com/spec/v1\n",
	} {
		t.Run(content[:5], func(t *testing.T) {
			f, api, _ := independentFixture(t)
			write(t, f.source, "blocked.txt", content)
			g(t, f.source, "add", ".")
			g(t, f.source, "commit", "--quiet", "-m", "Source fixture")
			_, err := startDeploymentReview(t, f, nil, api.call)
			if err == nil || strings.Contains(err.Error(), strings.Repeat("a", 30)) {
				t.Fatal("missing or unredacted blocker", err)
			}
			if api.creates != 0 {
				t.Fatal("created repository before source inspection")
			}
		})
	}
}

func TestIndependentRejectsInvalidOptionCombinations(t *testing.T) {
	for _, args := range [][]string{
		{"create", repo, "--repository-approve", strings.Repeat("a", 64)},
		{"create", repo, "--independent", "--fork"},
		{"create", repo, "--public", "--no-fork"},
		{"create", repo, "--repository", independentURL, "--use-source"},
		{"create", "--resume", ".", "--repository-approve", strings.Repeat("a", 64), "--approve", strings.Repeat("a", 64)},
		{"create", "--resume", ".", "--status", "--independent"},
		{"create", "--resume", ".", "--repository-approve", "bad"},
		{"create", "--public"},
		{"create", repo, "--public", "--private"},
		{"create", repo, "--private", "--fork"},
	} {
		var out strings.Builder
		if run(ctx, args, &out, nil) == nil {
			t.Fatal("accepted", args)
		}
	}
}
