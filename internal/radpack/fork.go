package radpack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
)

type githubPerson struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Type  string `json:"type"`
}
type githubFork struct {
	ID          int64        `json:"id"`
	FullName    string       `json:"full_name"`
	Fork        bool         `json:"fork"`
	Private     bool         `json:"private"`
	Archived    bool         `json:"archived"`
	Disabled    bool         `json:"disabled"`
	Owner       githubPerson `json:"owner"`
	Permissions struct {
		Admin bool `json:"admin"`
	} `json:"permissions"`
	Parent *struct {
		FullName string `json:"full_name"`
		ID       int64  `json:"id"`
	} `json:"parent"`
	Source *struct {
		FullName string `json:"full_name"`
		ID       int64  `json:"id"`
	} `json:"source"`
}
type ForkReview struct {
	Directory       string            `json:"directory"`
	CommonDirectory string            `json:"commonDirectory"`
	Application     Upstream          `json:"application"`
	Repository      string            `json:"repository"`
	Account         githubPerson      `json:"account"`
	Action          string            `json:"action"`
	RepositoryID    int64             `json:"repositoryId"`
	OriginalRemotes deploymentRemotes `json:"originalRemotes"`
	Worktrees       []string          `json:"affectedWorktrees"`
	Warning         string            `json:"warning"`
}
type ForkReport struct {
	ReportVersion int         `json:"reportVersion"`
	Command       string      `json:"command"`
	Status        string      `json:"status"`
	ReviewSHA256  string      `json:"reviewSha256,omitempty"`
	Review        *ForkReview `json:"review,omitempty"`
	Repository    string      `json:"repository,omitempty"`
	Diagnostics   []string    `json:"diagnostics"`
	NextAction    string      `json:"nextAction"`
}

func readGitHubUser(ctx context.Context, api githubAPI) (githubPerson, error) {
	status, body, err := api(ctx, "GET", "user", nil)
	if err != nil {
		return githubPerson{}, err
	}
	if status != 200 {
		return githubPerson{}, githubFailure(status)
	}
	var person githubPerson
	if json.Unmarshal(body, &person) != nil || person.ID <= 0 || person.Type != "User" ||
		!match(`^[A-Za-z0-9][A-Za-z0-9-]*$`, person.Login) {
		return githubPerson{}, fmt.Errorf("GitHub must identify an authenticated personal user, not an app or organization")
	}
	return person, nil
}
func readGitHubFork(ctx context.Context, api githubAPI, repository string) (*githubFork, error) {
	status, body, err := api(ctx, "GET", "repos/"+repoSlug(repository), nil)
	if err != nil {
		return nil, err
	}
	if status == 404 {
		return nil, nil
	}
	if status != 200 {
		return nil, githubFailure(status)
	}
	var fork githubFork
	if json.Unmarshal(body, &fork) != nil || fork.ID <= 0 || fork.FullName == "" {
		return nil, fmt.Errorf("GitHub returned invalid fork metadata; contents withheld")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return nil, fmt.Errorf("GitHub returned invalid fork metadata; contents withheld")
	}
	for _, name := range []string{"fork", "private", "archived", "disabled"} {
		var value *bool
		raw, exists := fields[name]
		if !exists || json.Unmarshal(raw, &value) != nil || value == nil {
			return nil, fmt.Errorf("GitHub did not report complete fork visibility and availability metadata")
		}
	}
	var permissions struct {
		Admin *bool `json:"admin"`
	}
	if json.Unmarshal(fields["permissions"], &permissions) != nil || permissions.Admin == nil {
		return nil, fmt.Errorf("GitHub did not report repository administrator permission")
	}
	return &fork, nil
}
func repoSlug(repository string) string {
	return strings.TrimSuffix(strings.TrimPrefix(repository, "https://github.com/"), ".git")
}
func validatePersonalFork(fork *githubFork, account githubPerson, source, destination string) error {
	if fork == nil {
		return fmt.Errorf("personal fork is not available yet")
	}
	if !strings.EqualFold(fork.FullName, repoSlug(destination)) || fork.Owner.ID != account.ID ||
		!strings.EqualFold(fork.Owner.Login, account.Login) || fork.Owner.Type != "User" {
		return fmt.Errorf("destination does not belong to the selected personal account")
	}
	if !fork.Fork {
		return fmt.Errorf("destination already exists but is not a GitHub fork; choose another --name")
	}
	if fork.Private || fork.Disabled || fork.Archived || !fork.Permissions.Admin {
		return fmt.Errorf("deployment fork must be public, active, and administrable by the selected credential")
	}
	parentMatches := fork.Parent != nil && fork.Parent.ID > 0 && strings.EqualFold(fork.Parent.FullName, repoSlug(source))
	rootMatches := fork.Source != nil && fork.Source.ID > 0 && strings.EqualFold(fork.Source.FullName, repoSlug(source))
	if !parentMatches && !rootMatches {
		return fmt.Errorf("destination is not a fork of the recorded application repository")
	}
	return nil
}

func forkApplication(ctx context.Context, root string) (Upstream, error) {
	if bound, err := readDeploymentBinding(ctx, root); err != nil {
		return Upstream{}, err
	} else if bound != nil {
		return bound.Application, nil
	}
	if author, err := loadAuthoring(ctx, root); err != nil {
		return Upstream{}, err
	} else if author != nil {
		return author.record.Upstream, nil
	}
	receipt, err := readRegular(root, receiptPath)
	if err != nil {
		return Upstream{}, err
	}
	if receipt != nil {
		if _, err := VerifyInstallation(ctx, "", root); err != nil {
			return Upstream{}, fmt.Errorf("installed snapshot must verify before fork setup: %w", err)
		}
		pack, err := LoadPack(filepath.Join(root, ".radpack", "pack"))
		if err != nil {
			return Upstream{}, err
		}
		return pack.Manifest.Upstream, nil
	}
	// A pending desktop clone has no receipt yet; its validated request still
	// establishes the source and baseline for the eventual pack application.
	handoff, err := loadHandoff(ctx, root)
	if err != nil {
		return Upstream{}, err
	}
	if handoff != nil {
		return handoff.pack.Manifest.Upstream, nil
	}
	remotes, err := readDeploymentRemotes(ctx, root)
	if err != nil {
		return Upstream{}, err
	}
	repository, err := repositoryURL(remotes.Origin)
	if err != nil {
		return Upstream{}, err
	}
	head, err := gitText(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return Upstream{}, err
	}
	return Upstream{repository, head}, nil
}

func forkWorktrees(ctx context.Context, root string) ([]string, error) {
	data, err := git(ctx, root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	result := []string{}
	for _, field := range splitNull(data) {
		if strings.HasPrefix(field, "worktree ") {
			result = append(result, strings.TrimPrefix(field, "worktree "))
		}
	}
	return uniqueSorted(result), nil
}

func planFork(ctx context.Context, root, name string, api githubAPI) (ForkReview, error) {
	root, err := repositoryRoot(ctx, root)
	if err != nil {
		return ForkReview{}, err
	}
	common, err := commonDirectory(ctx, root)
	if err != nil {
		return ForkReview{}, err
	}
	binding, err := readDeploymentBinding(ctx, root)
	if err != nil {
		return ForkReview{}, err
	}
	if binding != nil {
		return ForkReview{}, fmt.Errorf("fork setup already has a local record; resume the recorded operation")
	}
	source, err := forkApplication(ctx, root)
	if err != nil {
		return ForkReview{}, err
	}
	remotes, err := readDeploymentRemotes(ctx, root)
	if err != nil {
		return ForkReview{}, err
	}
	origin, err := repositoryURL(remotes.Origin)
	if err != nil {
		return ForkReview{}, err
	}
	if origin != source.Repository {
		return ForkReview{}, fmt.Errorf("origin no longer matches the recorded original application; resolve the repository identity before fork setup")
	}
	if remotes.Upstream != "" {
		upstream, err := repositoryURL(remotes.Upstream)
		if err != nil {
			return ForkReview{}, err
		}
		if upstream != source.Repository {
			return ForkReview{}, fmt.Errorf("existing upstream points to another repository; fork setup will not overwrite it")
		}
	}
	person, err := readGitHubUser(ctx, api)
	if err != nil {
		return ForkReview{}, err
	}
	if name == "" {
		name = strings.TrimSuffix(path.Base(source.Repository), ".git")
	}
	if !match(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`, name) || strings.HasSuffix(name, ".git") {
		return ForkReview{}, fmt.Errorf("fork --name must be a GitHub repository name, not a URL or owner/repository")
	}
	target, err := repositoryURL(person.Login + "/" + name)
	if err != nil {
		return ForkReview{}, err
	}
	if strings.EqualFold(source.Repository, target) {
		return ForkReview{}, fmt.Errorf("source already belongs to that personal repository; no separate fork is needed")
	}
	fork, err := readGitHubFork(ctx, api, target)
	if err != nil {
		return ForkReview{}, err
	}
	action := "create-personal-fork"
	id := int64(0)
	if fork != nil {
		if err := validatePersonalFork(fork, person, source.Repository, target); err != nil {
			return ForkReview{}, err
		}
		action = "reuse-personal-fork"
		id = fork.ID
	}
	worktrees, err := forkWorktrees(ctx, root)
	if err != nil {
		return ForkReview{}, err
	}
	return ForkReview{root, common, source, target, person, action, id, remotes, worktrees,
		"Public source forks are public. This changes origin for every linked worktree and preserves the original application as upstream. It does not push your local changes, start a session, enable Actions, or create environments. Reopen Radius explicitly for the fork and rerun readiness before deployment."}, nil
}

func forkReviewHash(review ForkReview) (string, error) {
	data, err := jsonBytes(review)
	if err != nil {
		return "", err
	}
	return hash(data), nil
}
func checkForkBaseline(ctx context.Context, repository, commit string) (err error) {
	directory, err := os.MkdirTemp("", "radpack-fork-check-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(directory)) }()
	// No checkout and no changes to the author's refs or current branch.
	if _, err := git(ctx, directory, "init", "--quiet", "--template="); err != nil {
		return err
	}
	if _, err := git(ctx, directory, "fetch", "--quiet", "--no-tags", "--depth=1", repository, commit); err != nil {
		return fmt.Errorf("fork is not ready to supply the recorded application commit; origin was not changed. Retry the recorded approval after GitHub finishes preparing the fork: %w", err)
	}
	actual, err := gitText(ctx, directory, "rev-parse", "FETCH_HEAD^{commit}")
	if err != nil {
		return err
	}
	if actual != commit {
		return fmt.Errorf("fork returned a different source revision")
	}
	return nil
}

func applyFork(ctx context.Context, review *ForkReview, record *deploymentBinding, approval string, api githubAPI) (*deploymentBinding, error) {
	root := ""
	if review != nil {
		root = review.Directory
	} else if record != nil {
		root = record.CommonDirectory
	}
	// Callers already hold an exclusive clone-local operation lock.
	if record == nil {
		fresh, err := planFork(ctx, root, strings.TrimSuffix(path.Base(review.Repository), ".git"), api)
		if err != nil {
			return nil, err
		}
		digest, err := forkReviewHash(fresh)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(fresh, *review) || digest != approval {
			return nil, fmt.Errorf("fork review changed; preview and approve again")
		}
		record = &deploymentBinding{FormatVersion: 1, CommonDirectory: review.CommonDirectory, Application: review.Application,
			Repository: review.Repository, Account: review.Account.Login, AccountID: review.Account.ID, RepositoryID: review.RepositoryID,
			Status: "pending", ReviewSHA256: approval, OriginalOrigin: review.OriginalRemotes.Origin, OriginalUpstream: review.OriginalRemotes.Upstream}
		if err := savePrivateJSON(record.CommonDirectory, deploymentBindingFile, *record); err != nil {
			return nil, err
		}
	} else {
		if record.ReviewSHA256 != approval {
			return record, fmt.Errorf("approval does not match the recorded fork operation")
		}
	}
	person, err := readGitHubUser(ctx, api)
	if err != nil {
		return record, err
	}
	if person.ID != record.AccountID || !strings.EqualFold(person.Login, record.Account) {
		return record, fmt.Errorf("selected GitHub identity changed; no account fallback is allowed")
	}
	// Remotes are shared by all worktrees. Address the common Git repository,
	// not a checkout that another session might switch during the operation.
	root = record.CommonDirectory
	remotes, err := readDeploymentRemotes(ctx, root)
	if err != nil {
		return record, err
	}
	if remotes.Origin != record.OriginalOrigin && remotes.Origin != record.Repository {
		return record, fmt.Errorf("origin changed outside this fork operation")
	}
	if remotes.Upstream != record.OriginalUpstream && remotes.Upstream != record.Application.Repository {
		return record, fmt.Errorf("upstream changed outside this fork operation")
	}
	fork, err := readGitHubFork(ctx, api, record.Repository)
	if err != nil {
		return record, err
	}
	if fork == nil {
		if record.RepositoryID != 0 {
			return record, fmt.Errorf("previously verified deployment fork is missing; refusing to recreate it")
		}
		if record.CreationAttempted {
			return record, fmt.Errorf("fork creation was already requested and its outcome is not confirmed. Wait for GitHub or inspect your account; no second fork request was sent")
		}
		record.CreationAttempted = true
		if err := writeDeploymentBinding(root, *record); err != nil {
			return record, err
		}
		status, _, err := api(ctx, "POST", "repos/"+repoSlug(record.Application.Repository)+"/forks", map[string]any{
			"name": strings.TrimSuffix(path.Base(record.Repository), ".git"), "default_branch_only": false,
		})
		if err != nil {
			return record, fmt.Errorf("fork creation outcome is unknown; inspect the recorded personal repository before retrying: %w", err)
		}
		if status != 202 && status != 201 {
			if status == 401 || status == 403 || status == 404 || status == 422 {
				// GitHub explicitly rejected creation and no local routing was
				// changed. Release this request so the user can choose another
				// fork name or account in a new review. Unknown outcomes retain it.
				if err := os.Remove(filepath.Join(root, deploymentBindingFile)); err != nil {
					return record, fmt.Errorf("GitHub rejected fork creation, but the local request could not be cleared: %w", err)
				}
				if status == 422 {
					return record, fmt.Errorf("GitHub rejected fork creation. Check whether your account already has this fork under another name and select it with --name; preview again before retrying")
				}
			}
			return record, githubFailure(status)
		}
		fork, err = readGitHubFork(ctx, api, record.Repository)
		if err != nil {
			return record, err
		}
		if fork == nil {
			return record, fmt.Errorf("GitHub accepted fork creation but the repository is not available yet; retry with the same approval")
		}
	}
	if err := validatePersonalFork(fork, person, record.Application.Repository, record.Repository); err != nil {
		return record, err
	}
	if record.RepositoryID != 0 && record.RepositoryID != fork.ID {
		return record, fmt.Errorf("deployment repository was replaced; refusing to use a different GitHub repository ID")
	}
	if err := checkForkBaseline(ctx, record.Repository, record.Application.Commit); err != nil {
		return record, err
	}
	// Persist the exact remote identity before changing local routing.
	record.RepositoryID = fork.ID
	if err := writeDeploymentBinding(root, *record); err != nil {
		return record, err
	}
	current, err := readDeploymentRemotes(ctx, root)
	if err != nil {
		return record, err
	}
	if current != remotes {
		return record, fmt.Errorf("Git remotes changed during fork setup; no local routing update was applied")
	}
	if current.Upstream == "" {
		if _, err := git(ctx, root, "remote", "add", "upstream", record.Application.Repository); err != nil {
			return record, err
		}
	}
	if current.Origin != record.Repository {
		if _, err := git(ctx, root, "remote", "set-url", "origin", record.Repository); err != nil {
			return record, err
		}
	}
	record.Status = "ready"
	if err := writeDeploymentBinding(root, *record); err != nil {
		return record, err
	}
	return record, nil
}

func forkCommand(ctx context.Context, values map[string][]string, out io.Writer, ui *createUI, api githubAPI) (resultErr error) {
	get := func(key string) string { return option(values, key) }
	report := ForkReport{1, "fork", "blocked", "", nil, "", []string{},
		"After setup, reopen Radius explicitly for the personal fork and rerun GitHub readiness. Commit/push prepared application code only with separate approval. No environments or cloud resources are created."}
	machine := get("json") != ""
	defer func() {
		if report.Repository != "" {
			report.NextAction = fmt.Sprintf("Reopen the SAME Radius canvas instance on its environment page with repo:%s, then run fresh GitHub readiness checks. Do not resume an operation started for the original repository. Commit/push prepared source only with separate approval; no environments or cloud resources were created.", repoSlug(report.Repository))
		}
		if resultErr != nil {
			report.Status = "blocked"
			report.Diagnostics = append(report.Diagnostics, resultErr.Error())
		}
		if machine {
			data, err := jsonBytes(report)
			if err == nil {
				_, err = out.Write(data)
			}
			resultErr = errors.Join(resultErr, err)
		} else if report.Status == "ready" {
			fmt.Fprintf(out, "Personal deployment fork configured: %s\n%s\n", report.Repository, report.NextAction)
		}
	}()
	if get("dry-run") != "" && get("approve") != "" {
		return fmt.Errorf("use --dry-run or --approve, not both")
	}
	if machine || get("dry-run") != "" || get("approve") != "" {
		ui = nil
	}
	directory := get("directory")
	if directory == "" {
		directory = "."
	}
	root, err := repositoryRoot(ctx, directory)
	if err != nil {
		return err
	}
	common, err := commonDirectory(ctx, root)
	if err != nil {
		return err
	}
	binding, err := readDeploymentBinding(ctx, root)
	if err != nil {
		return err
	}
	var review *ForkReview
	approval := get("approve")
	if binding == nil {
		plan, err := planFork(ctx, root, get("name"), api)
		if err != nil {
			return err
		}
		review = &plan
		report.Review = review
		report.Repository = plan.Repository
		report.ReviewSHA256, err = forkReviewHash(plan)
		if err != nil {
			return err
		}
		if !machine {
			fmt.Fprintf(out, "Application source: %s at %s\nDeployment fork: %s\nGitHub account: @%s\nAction: %s\n%s\nAffected linked checkouts:\n",
				plan.Application.Repository, plan.Application.Commit, plan.Repository, plan.Account.Login, plan.Action, plan.Warning)
			for _, worktree := range plan.Worktrees {
				fmt.Fprintf(out, "  %q\n", worktree)
			}
		}
	} else {
		if get("name") != "" && get("name") != strings.TrimSuffix(path.Base(binding.Repository), ".git") {
			return fmt.Errorf("a different fork operation is already recorded; finish it before choosing another destination")
		}
		report.Repository = binding.Repository
		report.ReviewSHA256 = binding.ReviewSHA256
		if binding.Status == "ready" {
			person, err := readGitHubUser(ctx, api)
			if err != nil {
				return err
			}
			if person.ID != binding.AccountID {
				return fmt.Errorf("selected GitHub identity differs from recorded fork account")
			}
			fork, err := readGitHubFork(ctx, api, binding.Repository)
			if err != nil {
				return err
			}
			if err := validatePersonalFork(fork, person, binding.Application.Repository, binding.Repository); err != nil {
				return err
			}
			if fork.ID != binding.RepositoryID {
				return fmt.Errorf("deployment fork repository ID changed")
			}
			if err := validateDeploymentRemotes(ctx, root, binding); err != nil {
				return err
			}
			report.Status = "ready"
			return nil
		}
		if !machine {
			fmt.Fprintf(out, "Resume pending fork setup: %s\nOriginal application: %s\n", binding.Repository, binding.Application.Repository)
		}
	}
	if ui != nil {
		yes, err := ui.confirm(ctx, "Create or reuse this personal fork and update shared Git remotes?")
		if errors.Is(err, errCreateCancelled) || (err == nil && !yes) {
			report.Status = "cancelled"
			fmt.Fprintln(out, "Cancelled. No fork setup changes made.")
			return nil
		}
		if err != nil {
			return err
		}
		approval = report.ReviewSHA256
	}
	if approval == "" {
		report.Status = "awaiting-approval"
		if !machine {
			fmt.Fprintf(out, "Review SHA: %s\nNo changes made. Repeat with --approve <review-sha> after reviewing the destination.\n", report.ReviewSHA256)
		}
		return nil
	}
	if approval != report.ReviewSHA256 {
		return fmt.Errorf("fork approval does not match the current review")
	}
	lock := filepath.Join(common, "radpack-fork.lock")
	claim, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("fork setup is already active or was interrupted; inspect the Git-local radpack-fork.lock before retrying")
	}
	if err := claim.Close(); err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, os.Remove(lock)) }()
	fresh, err := readDeploymentBinding(ctx, root)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(fresh, binding) {
		return fmt.Errorf("local fork setup state changed; preview again")
	}
	applied, err := applyFork(ctx, review, binding, approval, api)
	if applied != nil {
		report.Repository = applied.Repository
		report.ReviewSHA256 = applied.ReviewSHA256
	}
	if err != nil {
		return err
	}
	report.Status = "ready"
	return nil
}
