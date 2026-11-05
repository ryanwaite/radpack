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

func sourceAccessAPI(fake *fakeForkGitHub, admin bool, code int) githubAPI {
	return func(ctx context.Context, method, endpoint string, body any) (int, []byte, error) {
		if method == "GET" && endpoint == "repos/radpack-fixture/app" {
			fake.calls = append(fake.calls, method+" "+endpoint)
			if code != 200 {
				return code, []byte(`{"message":"access denied"}`), nil
			}
			source := publicFork(repo, repo)
			source.Fork = false
			source.Owner = githubPerson{Login: "radpack-fixture", ID: 80, Type: "Organization"}
			source.Permissions.Admin = admin
			b, _ := json.Marshal(source)
			return 200, b, nil
		}
		return fake.call(ctx, method, endpoint, body)
	}
}

func startDeploymentReview(t *testing.T, f *fixture, values map[string][]string, api githubAPI) (AuthoringReport, error) {
	t.Helper()
	options := cloneOptions(values)
	options["directory"] = []string{f.destination}
	options["output"] = []string{f.pack}
	options["json"] = []string{"true"}
	var out strings.Builder
	err := authoringWorkflowWithAPI(ctx, repo, options, &out, nil, true, api)
	var report AuthoringReport
	check(t, json.Unmarshal([]byte(out.String()), &report))
	return report, err
}

func resumeDeploymentReview(t *testing.T, f *fixture, values map[string][]string, api githubAPI) (AuthoringReport, error) {
	t.Helper()
	options := cloneOptions(values)
	options["resume"] = []string{f.destination}
	options["json"] = []string{"true"}
	var out strings.Builder
	err := authoringWorkflowWithAPI(ctx, "", options, &out, nil, true, api)
	var report AuthoringReport
	check(t, json.Unmarshal([]byte(out.String()), &report))
	return report, err
}

func TestCreateUseSourceSkipsForkForAdministrator(t *testing.T) {
	f := newAuthoringFixture(t)
	fake := forkAPI(t)
	report, err := startDeploymentReview(t, f, map[string][]string{"use-source": {"true"}}, sourceAccessAPI(fake, true, 200))
	check(t, err)
	if report.Status != "awaiting-radification" || report.RepositorySetup == nil ||
		report.RepositorySetup.Status != "ready" || report.RepositorySetup.Repository != repo {
		t.Fatal(report)
	}
	if fake.postCount != 0 {
		t.Fatal("admin source created fork")
	}
	binding, err := readDeploymentBinding(ctx, f.destination)
	check(t, err)
	if binding != nil || g(t, f.destination, "config", "--get", "remote.origin.url") != repo {
		t.Fatal("admin remotes changed")
	}
	report, err = resumeDeploymentReview(t, f, nil, sourceAccessAPI(fake, true, 200))
	check(t, err)
	if report.RepositorySetup.Status != "ready" || fake.postCount != 0 {
		t.Fatal("source choice not remembered", report)
	}
}

func TestCreateDefaultOffersForkAndResumesWithSeparateApproval(t *testing.T) {
	f := newAuthoringFixture(t)
	mapForkGit(t, f)
	fake := forkAPI(t)
	api := sourceAccessAPI(fake, false, 200)
	first, err := startDeploymentReview(t, f, nil, api)
	check(t, err)
	if first.Status != "awaiting-fork-approval" || first.RepositorySetup.Review.Action != "create-personal-fork" ||
		first.RepositorySetup.Review.Application.Repository != repo {
		t.Fatal(first)
	}
	if fake.postCount != 0 {
		t.Fatal("nonterminal implicitly approved")
	}
	sha := first.RepositorySetup.ReviewSHA256
	resumed, err := resumeDeploymentReview(t, f, map[string][]string{"fork-approve": {sha}}, api)
	check(t, err)
	if resumed.Status != "awaiting-radification" || resumed.RepositorySetup.Status != "ready" || fake.postCount != 1 {
		t.Fatal(resumed)
	}
	if g(t, f.destination, "config", "--get", "remote.origin.url") != personalForkURL {
		t.Fatal("fork not selected")
	}
	state, err := loadAuthoring(ctx, f.destination)
	check(t, err)
	if state.record.Upstream.Repository != repo || state.record.Upstream.Commit != f.base {
		t.Fatal("provenance changed")
	}
	again, err := resumeDeploymentReview(t, f, nil, api)
	check(t, err)
	if again.Status != "awaiting-radification" || fake.postCount != 1 {
		t.Fatal("resume duplicated fork")
	}
	radify(t, f.destination)
	var out strings.Builder
	check(t, authoringWorkflowWithAPI(ctx, "", map[string][]string{"resume": {f.destination}, "json": {"true"}}, &out, nil, true, api))
	var review CreateReport
	check(t, json.Unmarshal([]byte(out.String()), &review))
	if review.Status != "awaiting-review" || review.Review.Manifest.Upstream.Repository != repo {
		t.Fatal(out.String())
	}
	notCreated(t, f.pack)
	out.Reset()
	check(t, authoringWorkflowWithAPI(ctx, "", map[string][]string{
		"resume": {f.destination}, "json": {"true"}, "approve": {review.ReviewSHA256},
	}, &out, nil, true, api))
	pack, err := LoadPack(f.pack)
	check(t, err)
	if pack.Manifest.Upstream.Repository != repo || fake.postCount != 1 {
		t.Fatal("pack save changed provenance or repeated fork creation")
	}
}

func TestCreateDefaultNeverProbesSourceAdministration(t *testing.T) {
	for _, admin := range []bool{false, true} {
		t.Run(fmt.Sprint(admin), func(t *testing.T) {
			f := newAuthoringFixture(t)
			fake := forkAPI(t)
			api := sourceAccessAPI(fake, admin, 403)
			report, err := startDeploymentReview(t, f, nil, api)
			check(t, err)
			if report.Status != "awaiting-fork-approval" || fake.postCount != 0 {
				t.Fatal(report)
			}
			state, err := loadAuthoring(ctx, f.destination)
			check(t, err)
			choice, err := readAuthoringChoice(state)
			check(t, err)
			if choice.Mode != "fork" {
				t.Fatal(choice)
			}
			_, err = resumeDeploymentReview(t, f, nil, api)
			check(t, err)
			for _, call := range fake.calls {
				if call == "GET repos/radpack-fixture/app" {
					t.Fatal("default depended on upstream administration")
				}
			}
		})
	}
}

func TestCreateDefaultForkRefusalPreservesCheckout(t *testing.T) {
	f := newAuthoringFixture(t)
	fake := forkAPI(t)
	fake.postStatus = 403
	report, err := startDeploymentReview(t, f, nil, fake.call)
	check(t, err)
	_, err = resumeDeploymentReview(t, f, map[string][]string{"fork-approve": {report.RepositorySetup.ReviewSHA256}}, fake.call)
	fails(t, err, "GitHub refused access")
	if fake.postCount != 1 || g(t, f.destination, "config", "--get", "remote.origin.url") != repo ||
		g(t, f.destination, "status", "--porcelain") != "" {
		t.Fatal("rejected fork changed source or repeated creation")
	}
	state, err := loadAuthoring(ctx, f.destination)
	check(t, err)
	if state == nil {
		t.Fatal("lost authoring state")
	}
}

func TestCreateUseSourceDoesNotFallBackToFork(t *testing.T) {
	f := newAuthoringFixture(t)
	fake := forkAPI(t)
	api := sourceAccessAPI(fake, false, 200)
	report, err := startDeploymentReview(t, f, map[string][]string{"use-source": {"true"}}, api)
	check(t, err)
	if report.Status != "awaiting-github-access" || report.RepositorySetup.Review != nil {
		t.Fatal(report)
	}
	for _, call := range fake.calls {
		if call != "GET user" && call != "GET repos/radpack-fixture/app" {
			t.Fatal("source choice probed a fork", call)
		}
	}
}

func TestCreateRetainsLegacyAutoUntilExplicitForkChoice(t *testing.T) {
	f := newAuthoringFixture(t)
	prepareURL(t, f)
	state, err := loadAuthoring(ctx, f.destination)
	check(t, err)
	check(t, saveAuthoringChoice(state, authoringDeploymentChoice{1, "auto", ""}))
	fake := forkAPI(t)
	api := sourceAccessAPI(fake, false, 403)
	report, err := resumeDeploymentReview(t, f, nil, api)
	check(t, err)
	if report.Status != "awaiting-github-access" {
		t.Fatal("changed an existing workflow's policy", report)
	}
	report, err = resumeDeploymentReview(t, f, map[string][]string{"fork": {"true"}}, fake.call)
	check(t, err)
	if report.Status != "awaiting-fork-approval" || fake.postCount != 0 {
		t.Fatal(report)
	}
}

func TestCreateNamedForkPersistsAcrossResume(t *testing.T) {
	f := newAuthoringFixture(t)
	const named = "https://github.com/personal-user/app-radius.git"
	g(t, f.root, "config", "--global", "--add", "url."+filepath.ToSlash(f.source)+".insteadOf", named)
	fake := forkAPI(t)
	fake.repository = publicFork(repo, named)
	api := func(ctx context.Context, method, endpoint string, body any) (int, []byte, error) {
		if method == "GET" && endpoint == "repos/personal-user/app-radius" {
			return fake.call(ctx, method, "repos/personal-user/app", body)
		}
		return fake.call(ctx, method, endpoint, body)
	}
	first, err := startDeploymentReview(t, f, map[string][]string{"fork-name": {"app-radius"}}, api)
	check(t, err)
	if first.RepositorySetup.Repository != named || first.Status != "awaiting-fork-approval" {
		t.Fatal(first)
	}
	second, err := resumeDeploymentReview(t, f, nil, api)
	check(t, err)
	if second.RepositorySetup.ReviewSHA256 != first.RepositorySetup.ReviewSHA256 {
		t.Fatal("resume lost selected fork name")
	}
	_, err = resumeDeploymentReview(t, f, map[string][]string{"fork-approve": {second.RepositorySetup.ReviewSHA256}}, api)
	check(t, err)
	if g(t, f.destination, "config", "--get", "remote.origin.url") != named || fake.postCount != 0 {
		t.Fatal("named fork not reused")
	}
}

func TestCreateUseSourceRejectsUnrecordedOriginChange(t *testing.T) {
	f := newAuthoringFixture(t)
	fake := forkAPI(t)
	api := sourceAccessAPI(fake, true, 200)
	_, err := startDeploymentReview(t, f, map[string][]string{"use-source": {"true"}}, api)
	check(t, err)
	g(t, f.destination, "remote", "set-url", "origin", personalForkURL)
	_, err = resumeDeploymentReview(t, f, nil, api)
	fails(t, err, "without an approved deployment binding")
}

func TestCreateTerminalForkApprovalAndDecline(t *testing.T) {
	for _, approve := range []bool{false, true} {
		t.Run(map[bool]string{false: "decline-local", true: "approve"}[approve], func(t *testing.T) {
			f := newAuthoringFixture(t)
			mapForkGit(t, f)
			fake := forkAPI(t)
			api := sourceAccessAPI(fake, false, 200)
			answer := "\nq\n"
			if approve {
				answer = "y\nq\n"
			}
			var out strings.Builder
			ui := &createUI{bufio.NewReader(strings.NewReader(answer)), &out}
			check(t, authoringWorkflowWithAPI(ctx, repo, map[string][]string{"directory": {f.destination}, "output": {f.pack}}, &out, ui, true, api))
			if !strings.Contains(out.String(), "Visibility: public") || !strings.Contains(out.String(), "Go Radify your app") {
				t.Fatal(out.String())
			}
			if (fake.postCount == 1) != approve {
				t.Fatal(fake.postCount)
			}
			before := len(fake.calls)
			report, err := resumeDeploymentReview(t, f, nil, api)
			check(t, err)
			if report.Status != "awaiting-radification" {
				t.Fatal(report)
			}
			if !approve && len(fake.calls) != before {
				t.Fatal("remembered skip still accessed GitHub")
			}
		})
	}
}

func TestCreateForkEOFLeavesDecisionPending(t *testing.T) {
	f := newAuthoringFixture(t)
	fake := forkAPI(t)
	var out strings.Builder
	check(t, authoringWorkflowWithAPI(ctx, repo, map[string][]string{"directory": {f.destination}}, &out,
		&createUI{bufio.NewReader(strings.NewReader("")), &out}, true, sourceAccessAPI(fake, false, 200)))
	if fake.postCount != 0 {
		t.Fatal("EOF approved fork")
	}
	state, err := loadAuthoring(ctx, f.destination)
	check(t, err)
	choice, err := readAuthoringChoice(state)
	check(t, err)
	if choice.Mode != "fork" {
		t.Fatal("EOF silently skipped persistent choice")
	}
}

func TestCreateExplicitForkDeclineSurvivesRadificationContinuation(t *testing.T) {
	f := newAuthoringFixture(t)
	fake := forkAPI(t)
	var out strings.Builder
	check(t, authoringWorkflowWithAPI(ctx, repo, map[string][]string{
		"directory": {f.destination}, "fork": {"true"},
	}, &out, &createUI{bufio.NewReader(strings.NewReader("\n\n")), &out}, true, fake.call))
	if strings.Count(out.String(), "Create or reuse this personal fork for deployment?") != 1 || fake.postCount != 0 {
		t.Fatal(out.String())
	}
	state, err := loadAuthoring(ctx, f.destination)
	check(t, err)
	choice, err := readAuthoringChoice(state)
	check(t, err)
	if choice.Mode != "skip" {
		t.Fatal("continuation replaced the user's local-only choice")
	}
}

func TestCreateMissingAdminEvidenceRequiresAccessDecision(t *testing.T) {
	for _, permissions := range []string{`null`, `{}`, `{"admin":null}`} {
		t.Run(permissions, func(t *testing.T) {
			f := newAuthoringFixture(t)
			fake := forkAPI(t)
			base := sourceAccessAPI(fake, true, 200)
			api := func(ctx context.Context, method, endpoint string, body any) (int, []byte, error) {
				code, data, err := base(ctx, method, endpoint, body)
				if endpoint == "repos/radpack-fixture/app" {
					var fields map[string]json.RawMessage
					check(t, json.Unmarshal(data, &fields))
					fields["permissions"] = json.RawMessage(permissions)
					data, err = json.Marshal(fields)
				}
				return code, data, err
			}
			report, err := startDeploymentReview(t, f, map[string][]string{"use-source": {"true"}}, api)
			check(t, err)
			if report.Status != "awaiting-github-access" || report.RepositorySetup.Review != nil || fake.postCount != 0 {
				t.Fatal(report)
			}
		})
	}
}

func TestCreateNoForkAvoidsAllGitHubChecks(t *testing.T) {
	f := newAuthoringFixture(t)
	api := func(context.Context, string, string, any) (int, []byte, error) {
		t.Fatal("no-fork contacted GitHub API")
		return 0, nil, nil
	}
	report, err := startDeploymentReview(t, f, map[string][]string{"no-fork": {"true"}}, api)
	check(t, err)
	if report.RepositorySetup.Status != "skipped" || report.Status != "awaiting-radification" {
		t.Fatal(report)
	}
	report, err = resumeDeploymentReview(t, f, nil, api)
	check(t, err)
	if report.RepositorySetup.Status != "skipped" {
		t.Fatal(report)
	}
}

func TestCreateSAMLOrMissingAccessIsNotInsufficientPermission(t *testing.T) {
	for _, code := range []int{401, 403, 404, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			f := newAuthoringFixture(t)
			fake := forkAPI(t)
			report, err := startDeploymentReview(t, f, map[string][]string{"use-source": {"true"}}, sourceAccessAPI(fake, false, code))
			check(t, err)
			if report.Status != "awaiting-github-access" || fake.postCount != 0 || report.RepositorySetup.Review != nil {
				t.Fatal(report)
			}
			report, err = resumeDeploymentReview(t, f, map[string][]string{"no-fork": {"true"}}, fake.call)
			check(t, err)
			if report.Status != "awaiting-radification" {
				t.Fatal(report)
			}
		})
	}
}

func TestExplicitForkBypassesUpstreamAdminProbeNotConsent(t *testing.T) {
	f := newAuthoringFixture(t)
	fake := forkAPI(t)
	report, err := startDeploymentReview(t, f, map[string][]string{"fork": {"true"}}, fake.call)
	check(t, err)
	if report.Status != "awaiting-fork-approval" || fake.postCount != 0 {
		t.Fatal(report)
	}
	for _, call := range fake.calls {
		if call == "GET repos/radpack-fixture/app" {
			t.Fatal("explicit fork used upstream admin probe")
		}
	}
}

func TestCreateReuseForkStillRequiresApproval(t *testing.T) {
	f := newAuthoringFixture(t)
	mapForkGit(t, f)
	fake := forkAPI(t)
	fake.repository = publicFork(repo, personalForkURL)
	report, err := startDeploymentReview(t, f, map[string][]string{"fork": {"true"}}, fake.call)
	check(t, err)
	if report.RepositorySetup.Review.Action != "reuse-personal-fork" {
		t.Fatal(report)
	}
	if g(t, f.destination, "config", "--get", "remote.origin.url") != repo {
		t.Fatal("reuse implicitly changed remotes")
	}
	_, err = resumeDeploymentReview(t, f, map[string][]string{"fork-approve": {report.RepositorySetup.ReviewSHA256}}, fake.call)
	check(t, err)
	if fake.postCount != 0 {
		t.Fatal("reuse recreated fork")
	}
}

func TestCreatePendingForkCannotBeSkippedOrDuplicated(t *testing.T) {
	f := newAuthoringFixture(t)
	mapForkGit(t, f)
	fake := forkAPI(t)
	fake.hideCreated = true
	report, err := startDeploymentReview(t, f, map[string][]string{"fork": {"true"}}, fake.call)
	check(t, err)
	sha := report.RepositorySetup.ReviewSHA256
	_, err = resumeDeploymentReview(t, f, map[string][]string{"fork-approve": {sha}}, fake.call)
	fails(t, err, "not available yet")
	_, err = resumeDeploymentReview(t, f, map[string][]string{"no-fork": {"true"}}, fake.call)
	fails(t, err, "cannot undo or abandon")
	_, err = resumeDeploymentReview(t, f, map[string][]string{"use-source": {"true"}}, fake.call)
	fails(t, err, "cannot undo or abandon")
	report, err = resumeDeploymentReview(t, f, map[string][]string{"status": {"true"}}, nil)
	check(t, err)
	if report.Status != "awaiting-fork-approval" {
		t.Fatal(report)
	}
	fake.repository = fake.created
	report, err = resumeDeploymentReview(t, f, map[string][]string{"fork-approve": {sha}}, fake.call)
	check(t, err)
	if fake.postCount != 1 || report.RepositorySetup.Status != "ready" {
		t.Fatal(report)
	}
}

func TestCreateForkApprovalRejectsDrift(t *testing.T) {
	f := newAuthoringFixture(t)
	fake := forkAPI(t)
	report, err := startDeploymentReview(t, f, map[string][]string{"fork": {"true"}}, fake.call)
	check(t, err)
	fake.user.ID++
	_, err = resumeDeploymentReview(t, f, map[string][]string{"fork-approve": {report.RepositorySetup.ReviewSHA256}}, fake.call)
	fails(t, err, "approval")
	if fake.postCount != 0 {
		t.Fatal("stale account approval created fork")
	}
}

func TestCreateStatusDoesNotProbeOrChangeChoice(t *testing.T) {
	f := newAuthoringFixture(t)
	fake := forkAPI(t)
	_, err := startDeploymentReview(t, f, nil, sourceAccessAPI(fake, false, 200))
	check(t, err)
	state, err := loadAuthoring(ctx, f.destination)
	check(t, err)
	before := read(t, state.directory, "deployment-choice.json")
	api := func(context.Context, string, string, any) (int, []byte, error) {
		t.Fatal("status probed")
		return 0, nil, nil
	}
	report, err := resumeDeploymentReview(t, f, map[string][]string{"status": {"true"}}, api)
	check(t, err)
	if report.Status != "awaiting-deployment-choice" || read(t, state.directory, "deployment-choice.json") != before {
		t.Fatal(report)
	}
}

func TestOldAuthoringRecordsReceiveGuidedSetup(t *testing.T) {
	f := newAuthoringFixture(t)
	prepareURL(t, f)
	state, err := loadAuthoring(ctx, f.destination)
	check(t, err)
	check(t, os.Remove(filepath.Join(state.directory, "deployment-choice.json")))
	fake := forkAPI(t)
	report, err := resumeDeploymentReview(t, f, nil, sourceAccessAPI(fake, false, 200))
	check(t, err)
	if report.Status != "awaiting-fork-approval" {
		t.Fatal(report)
	}
}

func TestStandaloneForkOverridesRememberedLocalChoice(t *testing.T) {
	f := newAuthoringFixture(t)
	mapForkGit(t, f)
	prepareURL(t, f)
	fake := forkAPI(t)
	review := previewFork(t, f.destination, fake)
	_, err := approvedFork(t, f.destination, review.ReviewSHA256, fake)
	check(t, err)
	report, err := resumeDeploymentReview(t, f, nil, fake.call)
	check(t, err)
	if report.RepositorySetup.Status != "ready" {
		t.Fatal(report)
	}
}

func TestCreateDeploymentOptionsRejectUnsafeCombinations(t *testing.T) {
	for _, args := range [][]string{
		{"create", repo, "--fork", "--no-fork"},
		{"create", repo, "--no-fork", "--fork-name", "example"},
		{"create", repo, "--fork-name", "owner/repo"},
		{"create", repo, "--fork-approve", strings.Repeat("a", 64)},
		{"create", "--fork"},
		{"create", "--resume", ".", "--fork-approve", "bad"},
		{"create", "--resume", ".", "--fork-approve", strings.Repeat("a", 64), "--approve", strings.Repeat("a", 64)},
		{"create", "--resume", ".", "--fork-approve", strings.Repeat("a", 64), "--dry-run"},
		{"create", "--resume", ".", "--status", "--fork"},
		{"create", "--resume", ".", "--status", "--no-fork"},
		{"create", "--resume", ".", "--status", "--fork-name", "example"},
		{"create", repo, "--use-source", "--fork"},
		{"create", repo, "--use-source", "--no-fork"},
		{"create", repo, "--use-source", "--fork-name", "example"},
		{"create", "--resume", ".", "--use-source", "--fork-approve", strings.Repeat("a", 64)},
		{"create", "--resume", ".", "--status", "--use-source"},
		{"create", "--use-source"},
	} {
		var out strings.Builder
		if run(ctx, args, &out, nil) == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestCreateDeploymentFailurePreservesClone(t *testing.T) {
	f := newAuthoringFixture(t)
	fake := forkAPI(t)
	fake.readErr = errors.New("GitHub unavailable")
	report, err := startDeploymentReview(t, f, map[string][]string{"use-source": {"true"}}, fake.call)
	check(t, err)
	if report.Status != "awaiting-github-access" {
		t.Fatal(report)
	}
	if g(t, f.destination, "status", "--porcelain") != "" {
		t.Fatal("source checkout changed")
	}
	state, err := loadAuthoring(ctx, f.destination)
	check(t, err)
	if state == nil {
		t.Fatal("lost authoring workflow")
	}
}
