package radpack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const authoringBranch = "refs/heads/radpack-authoring"

type RepositoryReview struct {
	CommonDirectory string            `json:"commonDirectory"`
	Application     Upstream          `json:"application"`
	Repository      string            `json:"repository"`
	Account         githubPerson      `json:"account"`
	RepositoryID    int64             `json:"repositoryId"`
	Private         bool              `json:"private"`
	Action          string            `json:"action"`
	Tree            string            `json:"tree"`
	Branch          string            `json:"branch"`
	OriginalRemotes deploymentRemotes `json:"originalRemotes"`
	Worktrees       []string          `json:"affectedWorktrees"`
	Files           []snapshotFile    `json:"files"`
	Warnings        []string          `json:"warnings"`
	LocalHead       string            `json:"localHead"`
	LocalBranch     string            `json:"localBranch"`
}

type snapshotFile struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

func repositoryReviewHash(review RepositoryReview) (string, error) {
	data, err := jsonBytes(review)
	if err != nil {
		return "", err
	}
	return hash(data), nil
}

// Only the literal tutorial pair on an exact loopback host is downgraded.
// Inspect every match: an example must never mask a second, real credential.
func onlyLocalTutorialURLs(text, pattern string) bool {
	matches := regexp.MustCompile(pattern).FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return false
	}
	for _, match := range matches {
		end := match[1]
		for end < len(text) && !strings.ContainsRune("/?# \t\r\n\"'`<>", rune(text[end])) {
			end++
		}
		parsed, err := url.Parse(text[match[0]:end])
		if err != nil || parsed.User == nil || parsed.User.Username() != "username" {
			return false
		}
		password, ok := parsed.User.Password()
		if !ok || password != "password" {
			return false
		}
		switch strings.ToLower(parsed.Hostname()) {
		case "localhost", "127.0.0.1", "::1":
		default:
			return false
		}
	}
	return true
}

// Inspect only the recorded upstream tree, never the worktree or its index.
// A new root commit deliberately excludes all upstream and local history.
func inspectSnapshot(ctx context.Context, root string, source Upstream) (string, []snapshotFile, []string, error) {
	tree, err := gitText(ctx, root, "rev-parse", source.Commit+"^{tree}")
	if err != nil {
		return "", nil, nil, err
	}
	entries, err := treeAt(ctx, root, source.Commit)
	if err != nil {
		return "", nil, nil, err
	}
	paths := make([]string, 0, len(entries))
	for file := range entries {
		paths = append(paths, file)
	}
	sort.Strings(paths)
	files := []snapshotFile{}
	warnings := []string{
		"Publishes the pinned source snapshot, including binary assets and license/notice files, as one new root commit. No upstream history, local changes, other branches, or tags are pushed.",
		"Confirm that you have permission to redistribute these files. Secret detection is best-effort, not a certification. Binary assets are not semantically inspected.",
		"GitHub Actions will be disabled and verified disabled before publication, including in an explicitly selected empty repository. Radpack does not re-enable Actions.",
		"After publication, this checkout switches to a new radpack-authoring branch tracking origin/radpack-authoring. The previous local branch and uncommitted files are preserved; other linked worktrees are not switched.",
	}
	total := 0
	for _, file := range paths {
		entry := entries[file]
		if err := safePath(file); err != nil {
			return "", nil, nil, fmt.Errorf("snapshot path cannot be safely published: %w", err)
		}
		if entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") {
			return "", nil, nil, fmt.Errorf("snapshot contains unsupported symlink or submodule at %s; prepare this repository manually", file)
		}
		size, err := gitText(ctx, root, "cat-file", "-s", entry.Object)
		if err != nil {
			return "", nil, nil, err
		}
		var count int
		if _, err := fmt.Sscan(size, &count); err != nil || count < 0 || count > maxFileBytes {
			return "", nil, nil, fmt.Errorf("snapshot file exceeds inspection limits: %s", file)
		}
		total += count
		if total > maxPackBytes {
			return "", nil, nil, fmt.Errorf("snapshot exceeds the 32 MiB inspection limit; prepare this repository manually")
		}
		data, err := git(ctx, root, "cat-file", "blob", entry.Object)
		if err != nil {
			return "", nil, nil, err
		}
		if strings.HasPrefix(string(data), "version https://git-lfs.github.com/spec/v1") {
			return "", nil, nil, fmt.Errorf("snapshot contains Git LFS pointer %s; copying its pointer would not publish the asset", file)
		}
		localTutorialURL := false
		// These signatures block publication even in binary files.
		for _, rule := range contentRules {
			switch rule.name {
			case "private key", "GitHub token", "npm token", "AWS access key", "credential-bearing URL", "JWT-like token", "SAS or storage credential":
				if match(rule.pattern, string(data)) {
					if rule.name == "credential-bearing URL" && utf8.Valid(data) &&
						!strings.ContainsRune(string(data), '\x00') && onlyLocalTutorialURLs(string(data), rule.pattern) {
						localTutorialURL = true
						continue
					}
					return "", nil, nil, fmt.Errorf("source-publication blocker: %s detected in %s (value withheld)", rule.name, file)
				}
			}
		}
		reason, err := excludedReason(file)
		if err != nil {
			return "", nil, nil, err
		}
		if reason != "" {
			warnings = append(warnings, fmt.Sprintf("Review upstream file %s: %s. Snapshot publication includes it; this is not a portable pack export.", file, reason))
		}
		if utf8.Valid(data) && !strings.ContainsRune(string(data), '\x00') {
			findings, err := inspectContent(data, file)
			if err != nil {
				return "", nil, nil, err
			}
			for _, finding := range findings {
				if finding == "credential-bearing URL" && localTutorialURL {
					finding = "local tutorial credential URL; verify these example credentials are not used for a real service"
				}
				warnings = append(warnings, fmt.Sprintf("Review upstream file %s: %s (value withheld).", file, finding))
			}
		}
		files = append(files, snapshotFile{file, entry.Mode, hash(data), len(data)})
	}
	return tree, files, warnings, nil
}

func validateIndependentRepository(repository *githubFork, account githubPerson, target string, private bool) error {
	if repository == nil {
		return fmt.Errorf("independent repository is not visible yet; inspect the recorded destination before retrying")
	}
	if !strings.EqualFold(repository.FullName, repoSlug(target)) || repository.Owner.ID != account.ID ||
		!strings.EqualFold(repository.Owner.Login, account.Login) || repository.Owner.Type != "User" ||
		repository.Fork || repository.Archived || repository.Disabled || !repository.Permissions.Admin ||
		repository.Private != private {
		return fmt.Errorf("destination must be an active independent repository owned and administered by the selected personal account with the reviewed visibility")
	}
	return nil
}

func repositoryRefs(ctx context.Context, target string) (map[string]string, error) {
	data, err := gitText(ctx, "", "ls-remote", "--refs", target)
	if err != nil {
		return nil, fmt.Errorf("could not inspect destination refs: %w", err)
	}
	refs := map[string]string{}
	if data == "" {
		return refs, nil
	}
	for _, line := range strings.Split(data, "\n") {
		sha, ref, ok := strings.Cut(line, "\t")
		if !ok || !commitPattern.MatchString(sha) || !strings.HasPrefix(ref, "refs/") {
			return nil, fmt.Errorf("destination returned invalid refs")
		}
		refs[ref] = sha
	}
	return refs, nil
}

func planRepository(ctx context.Context, state *authoringState, choice authoringDeploymentChoice, api githubAPI) (RepositoryReview, error) {
	review := RepositoryReview{CommonDirectory: state.record.CommonDirectory, Application: state.record.Upstream,
		Private: !choice.Public, Branch: authoringBranch, Action: "create-independent-repository"}
	var err error
	review.OriginalRemotes, err = readDeploymentRemotes(ctx, state.record.SourceDirectory)
	if err != nil {
		return review, err
	}
	origin, err := repositoryURL(review.OriginalRemotes.Origin)
	if err != nil || origin != review.Application.Repository {
		return review, fmt.Errorf("origin differs from the recorded application; no repository will be published")
	}
	if review.OriginalRemotes.Upstream != "" {
		upstream, err := repositoryURL(review.OriginalRemotes.Upstream)
		if err != nil || upstream != review.Application.Repository {
			return review, fmt.Errorf("existing upstream does not match the application; it will not be overwritten")
		}
	}
	review.Account, err = readGitHubUser(ctx, api)
	if err != nil {
		return review, err
	}
	target := choice.Repository
	if target == "" {
		target = review.Account.Login + "/" + strings.TrimSuffix(path.Base(review.Application.Repository), ".git") + "-radpack"
	}
	review.Repository, err = repositoryURL(target)
	if err != nil {
		return review, err
	}
	if !strings.EqualFold(strings.Split(repoSlug(review.Repository), "/")[0], review.Account.Login) ||
		strings.EqualFold(review.Repository, review.Application.Repository) {
		return review, fmt.Errorf("independent destination must be a different repository in your selected personal account")
	}
	repository, err := readGitHubFork(ctx, api, review.Repository)
	if err != nil {
		return review, fmt.Errorf("inspect independent destination: %w", err)
	}
	if repository != nil {
		if choice.Repository == "" {
			return review, fmt.Errorf("suggested destination already exists; explicitly select it with --repository only if it is empty, or choose another name")
		}
		if err := validateIndependentRepository(repository, review.Account, review.Repository, review.Private); err != nil {
			return review, err
		}
		refs, err := repositoryRefs(ctx, review.Repository)
		if err != nil {
			return review, err
		}
		if len(refs) != 0 {
			return review, fmt.Errorf("existing destination is not empty; Radpack will not overwrite or add source to it")
		}
		review.RepositoryID = repository.ID
		review.Action = "populate-explicit-empty-repository"
	}
	review.Worktrees, err = forkWorktrees(ctx, state.record.SourceDirectory)
	if err != nil {
		return review, err
	}
	review.Tree, review.Files, review.Warnings, err = inspectSnapshot(ctx, state.record.SourceDirectory, review.Application)
	if err != nil {
		return review, err
	}
	review.LocalHead, err = gitText(ctx, state.record.SourceDirectory, "rev-parse", "HEAD")
	if err != nil {
		return review, err
	}
	review.LocalBranch, err = gitText(ctx, state.record.SourceDirectory, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return review, fmt.Errorf("independent setup requires an attached branch; preserve your work on a branch first")
	}
	headTree, err := gitText(ctx, state.record.SourceDirectory, "rev-parse", "HEAD^{tree}")
	if err != nil || headTree != review.Tree {
		return review, fmt.Errorf("checkout HEAD differs from the pinned source tree; preserve committed authoring work and select a checkout at the original baseline before independent setup")
	}
	existing, err := gitText(ctx, state.record.SourceDirectory, "for-each-ref", "--format=%(refname)", "refs/heads/radpack-authoring")
	if err != nil || existing != "" {
		return review, fmt.Errorf("local radpack-authoring branch already exists or cannot be inspected; it will not be overwritten")
	}
	return review, nil
}

func verifySnapshotCommit(ctx context.Context, root string, binding *deploymentBinding) error {
	tree, err := gitText(ctx, root, "rev-parse", binding.SnapshotCommit+"^{tree}")
	if err != nil || tree != binding.SnapshotTree {
		return fmt.Errorf("recorded publication commit does not match the reviewed source tree")
	}
	parents, err := gitText(ctx, root, "rev-list", "--parents", "-n", "1", binding.SnapshotCommit)
	if err != nil || parents != binding.SnapshotCommit {
		return fmt.Errorf("publication commit must be a root snapshot without history")
	}
	return nil
}

func actionsDisabled(ctx context.Context, target string, api githubAPI) error {
	status, data, err := api(ctx, "GET", "repos/"+repoSlug(target)+"/actions/permissions", nil)
	if err != nil {
		return fmt.Errorf("check destination Actions policy: %w", err)
	}
	if status != 200 {
		return fmt.Errorf("check destination Actions policy: %w", githubFailure(status))
	}
	var policy struct {
		Enabled *bool `json:"enabled"`
	}
	if json.Unmarshal(data, &policy) != nil || policy.Enabled == nil || *policy.Enabled {
		return fmt.Errorf("destination Actions must be confirmed disabled before source publication")
	}
	return nil
}

func confirmPublicationAccount(ctx context.Context, expected githubPerson, api githubAPI) error {
	current, err := readGitHubUser(ctx, api)
	if err != nil {
		return err
	}
	if current != expected {
		return fmt.Errorf("selected GitHub account changed; publication approval is no longer valid")
	}
	return nil
}

func applyRepository(ctx context.Context, state *authoringState, review RepositoryReview, binding *deploymentBinding, api githubAPI) (*deploymentBinding, error) {
	root := state.record.CommonDirectory
	if binding == nil {
		digest, err := repositoryReviewHash(review)
		if err != nil {
			return nil, err
		}
		binding = &deploymentBinding{FormatVersion: 1, CommonDirectory: root, Application: review.Application,
			Repository: review.Repository, Account: review.Account.Login, AccountID: review.Account.ID, RepositoryID: review.RepositoryID,
			Status: "pending", ReviewSHA256: digest, OriginalOrigin: review.OriginalRemotes.Origin, OriginalUpstream: review.OriginalRemotes.Upstream,
			Kind: "independent", Private: review.Private, SnapshotTree: review.Tree, PublicationReview: &review}
		if err := savePrivateJSON(root, deploymentBindingFile, binding); err != nil {
			return nil, err
		}
	}
	person, err := readGitHubUser(ctx, api)
	if err != nil {
		return binding, err
	}
	if person != review.Account {
		return binding, fmt.Errorf("selected GitHub account changed; no publication was authorized for this account")
	}
	worktrees, err := forkWorktrees(ctx, state.record.SourceDirectory)
	if err != nil || !reflect.DeepEqual(worktrees, review.Worktrees) {
		return binding, fmt.Errorf("linked worktrees changed since repository approval; inspect the pending operation")
	}
	remotes, err := readDeploymentRemotes(ctx, root)
	if err != nil {
		return binding, err
	}
	if (remotes.Origin != binding.OriginalOrigin && remotes.Origin != binding.Repository) ||
		(remotes.Upstream != binding.OriginalUpstream && remotes.Upstream != binding.Application.Repository) {
		return binding, fmt.Errorf("remotes changed outside the recorded repository operation")
	}
	tree, files, warnings, err := inspectSnapshot(ctx, root, review.Application)
	if err != nil {
		return binding, err
	}
	if tree != review.Tree || !reflect.DeepEqual(files, review.Files) || !reflect.DeepEqual(warnings, review.Warnings) {
		return binding, fmt.Errorf("publication source differs from the approved snapshot")
	}
	if binding.SnapshotCommit == "" {
		sign, err := gitText(ctx, root, "config", "--type=bool", "--default=false", "--get", "commit.gpgsign")
		if err != nil {
			return binding, err
		}
		args := []string{"commit-tree", review.Tree}
		if sign == "true" {
			args = append(args, "-S")
		}
		args = append(args, "-m", fmt.Sprintf("Import pinned application snapshot\n\nUpstream: %s\nUpstream-Commit: %s\n\nOriginal license and attribution files retained. Upstream history and local changes are not included.", review.Application.Repository, review.Application.Commit))
		binding.SnapshotCommit, err = gitText(ctx, root, args...)
		if err != nil {
			return binding, fmt.Errorf("prepare snapshot commit (configured signing is honored): %w", err)
		}
		if err := writeDeploymentBinding(root, *binding); err != nil {
			return binding, err
		}
	}
	if err := verifySnapshotCommit(ctx, root, binding); err != nil {
		return binding, err
	}
	// Retain the root commit for retries without changing any worktree branch.
	if _, err := git(ctx, root, "update-ref", "refs/radpack/publication/"+binding.ReviewSHA256, binding.SnapshotCommit); err != nil {
		return binding, err
	}
	repository, err := readGitHubFork(ctx, api, review.Repository)
	if err != nil {
		return binding, fmt.Errorf("inspect destination before publication: %w", err)
	}
	if repository == nil {
		if binding.RepositoryID != 0 || binding.CreationAttempted {
			return binding, fmt.Errorf("repository creation is missing or uncertain; inspect the recorded destination, no second creation request was sent")
		}
		if err := confirmPublicationAccount(ctx, review.Account, api); err != nil {
			return binding, err
		}
		binding.CreationAttempted = true
		if err := writeDeploymentBinding(root, *binding); err != nil {
			return binding, err
		}
		status, data, err := api(ctx, "POST", "user/repos", map[string]any{
			"name": strings.TrimSuffix(path.Base(review.Repository), ".git"), "private": review.Private, "auto_init": false,
		})
		if err != nil {
			return binding, fmt.Errorf("POST user/repos outcome is unknown; inspect the recorded destination before retrying: %w", err)
		}
		if status != 201 {
			if status == 401 || status == 403 || status == 404 || status == 422 {
				if err := os.Remove(filepath.Join(root, deploymentBindingFile)); err != nil {
					return binding, fmt.Errorf("repository creation rejected; could not clear journal: %w", err)
				}
			}
			return binding, fmt.Errorf("POST user/repos (create independent repository): %w", githubFailure(status))
		}
		// Only the creation response establishes ownership of a newly created
		// repository. A matching name after an ambiguous response is insufficient.
		var created githubFork
		if json.Unmarshal(data, &created) != nil || created.ID <= 0 || !strings.EqualFold(created.FullName, repoSlug(review.Repository)) {
			return binding, fmt.Errorf("repository creation returned no trustworthy identity; inspect the destination manually")
		}
		binding.RepositoryID = created.ID
		if err := writeDeploymentBinding(root, *binding); err != nil {
			return binding, err
		}
		repository, err = readGitHubFork(ctx, api, review.Repository)
		if err != nil {
			return binding, err
		}
	}
	if binding.RepositoryID == 0 {
		return binding, fmt.Errorf("destination appeared without a confirmed creation identity; refusing to claim or populate it")
	}
	if err := validateIndependentRepository(repository, person, review.Repository, review.Private); err != nil {
		return binding, err
	}
	if repository.ID != binding.RepositoryID {
		return binding, fmt.Errorf("independent repository identity changed")
	}
	refs, err := repositoryRefs(ctx, review.Repository)
	if err != nil {
		return binding, err
	}
	published := len(refs) == 1 && refs[authoringBranch] == binding.SnapshotCommit && binding.PushAttempted
	if len(refs) != 0 && !published {
		return binding, fmt.Errorf("destination refs changed; no source will be overwritten")
	}
	if !published {
		if err := confirmPublicationAccount(ctx, review.Account, api); err != nil {
			return binding, err
		}
		status, _, err := api(ctx, "PUT", "repos/"+repoSlug(review.Repository)+"/actions/permissions", map[string]any{"enabled": false})
		if err != nil {
			return binding, fmt.Errorf("disable destination Actions before push: %w", err)
		}
		if status != 204 {
			return binding, fmt.Errorf("disable destination Actions before push: %w", githubFailure(status))
		}
		if err := actionsDisabled(ctx, review.Repository, api); err != nil {
			return binding, err
		}
		// Confirm identity/visibility and refs again after changing Actions policy.
		current, err := readGitHubFork(ctx, api, review.Repository)
		if err != nil {
			return binding, err
		}
		if err := validateIndependentRepository(current, person, review.Repository, review.Private); err != nil {
			return binding, err
		}
		if current.ID != binding.RepositoryID {
			return binding, fmt.Errorf("destination identity changed before push")
		}
		refs, err = repositoryRefs(ctx, review.Repository)
		if err != nil {
			return binding, err
		}
		if len(refs) != 0 {
			return binding, fmt.Errorf("destination is no longer confirmed empty; push refused")
		}
		if err := confirmPublicationAccount(ctx, review.Account, api); err != nil {
			return binding, err
		}
		binding.PushAttempted = true
		if err := writeDeploymentBinding(root, *binding); err != nil {
			return binding, err
		}
		// The explicit empty lease rejects even a concurrently created ancestor.
		// Disable automatic tag/submodule pushes; publish exactly one root commit.
		if _, err := git(ctx, root, "-c", "push.followTags=false", "push", "--porcelain", "--recurse-submodules=no",
			"--force-with-lease="+authoringBranch+":", review.Repository, binding.SnapshotCommit+":"+authoringBranch); err != nil {
			return binding, fmt.Errorf("snapshot push outcome is uncertain; resume to inspect remote refs, not to overwrite them: %w", err)
		}
	}
	refs, err = repositoryRefs(ctx, review.Repository)
	if err != nil || len(refs) != 1 || refs[authoringBranch] != binding.SnapshotCommit {
		return binding, fmt.Errorf("remote snapshot publication is not confirmed; inspect the recorded operation")
	}
	if err := actionsDisabled(ctx, review.Repository, api); err != nil {
		return binding, err
	}
	confirmed, err := readGitHubFork(ctx, api, review.Repository)
	if err != nil {
		return binding, err
	}
	if err := validateIndependentRepository(confirmed, person, review.Repository, review.Private); err != nil {
		return binding, err
	}
	if confirmed.ID != binding.RepositoryID {
		return binding, fmt.Errorf("destination identity changed after source publication")
	}
	current, err := readDeploymentRemotes(ctx, root)
	if err != nil || current != remotes {
		return binding, fmt.Errorf("local remotes changed during publication; source is published but routing was not completed")
	}
	if current.Upstream == "" {
		if _, err := git(ctx, root, "remote", "add", "upstream", review.Application.Repository); err != nil {
			return binding, err
		}
	}
	if current.Origin != review.Repository {
		if _, err := git(ctx, root, "remote", "set-url", "origin", review.Repository); err != nil {
			return binding, err
		}
	}
	head, err := gitText(ctx, state.record.SourceDirectory, "rev-parse", "HEAD")
	if err != nil {
		return binding, err
	}
	branch, err := gitText(ctx, state.record.SourceDirectory, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return binding, err
	}
	if branch != "radpack-authoring" || head != binding.SnapshotCommit {
		if head != review.LocalHead || branch != review.LocalBranch {
			return binding, fmt.Errorf("snapshot is published but local branch changed; refusing to switch authoring context")
		}
		if _, err := git(ctx, state.record.SourceDirectory, "checkout", "--quiet", "-b", "radpack-authoring", binding.SnapshotCommit); err != nil {
			return binding, fmt.Errorf("snapshot is published but authoring branch setup failed; existing work was not forced or overwritten: %w", err)
		}
	}
	if _, err := git(ctx, root, "fetch", "--quiet", "--no-tags", "--depth=1", review.Repository, authoringBranch+":refs/remotes/origin/radpack-authoring"); err != nil {
		return binding, err
	}
	if _, err := git(ctx, state.record.SourceDirectory, "branch", "--set-upstream-to=origin/radpack-authoring", "radpack-authoring"); err != nil {
		return binding, err
	}
	binding.Status = "ready"
	return binding, writeDeploymentBinding(root, *binding)
}

func prepareIndependentRepository(ctx context.Context, state *authoringState, choice authoringDeploymentChoice, binding *deploymentBinding, values map[string][]string, ui *createUI, api githubAPI) (result authoringDeployment, resultErr error) {
	result.Status = "awaiting-repository-approval"
	result.NextAction = "Review repositorySetup.publicationReview and resume with --repository-approve <reviewSha256>. This approves repository creation/reuse, disabling Actions, and publication of the pinned source snapshot, not pack saving."
	defer func() {
		if resultErr != nil {
			result.Status = "blocked"
			result.Message = resultErr.Error()
		}
		current, err := readDeploymentBinding(ctx, state.record.SourceDirectory)
		if err != nil {
			resultErr = errors.Join(resultErr, err)
			result.Status = "blocked"
		} else if current != nil && current.Kind == "independent" {
			if current.Status == "ready" {
				result.SourcePublication = "confirmed"
			} else if current.PushAttempted {
				result.SourcePublication = "unconfirmed"
			}
		}
	}()
	result.SourcePublication = "not-performed"
	var review RepositoryReview
	var err error
	if binding != nil {
		if binding.Kind != "independent" {
			return result, fmt.Errorf("a fork operation is already recorded; it cannot be silently replaced with independent publication")
		}
		review = *binding.PublicationReview
		if (choice.Repository != "" && choice.Repository != binding.Repository) || choice.Public == binding.Private {
			return result, fmt.Errorf("repository name or visibility differs from the recorded operation")
		}
	} else {
		review, err = planRepository(ctx, state, choice, api)
		if err != nil {
			return result, err
		}
	}
	result.Repository = review.Repository
	result.PublicationReview = &review
	result.ReviewSHA256, err = repositoryReviewHash(review)
	if err != nil {
		return result, err
	}
	if binding != nil && binding.Status == "ready" {
		person, err := readGitHubUser(ctx, api)
		if err != nil {
			return result, err
		}
		if person != review.Account {
			return result, fmt.Errorf("selected account differs from repository setup")
		}
		repository, err := readGitHubFork(ctx, api, review.Repository)
		if err != nil {
			return result, err
		}
		if err := validateIndependentRepository(repository, person, review.Repository, review.Private); err != nil {
			return result, err
		}
		if repository.ID != binding.RepositoryID {
			return result, fmt.Errorf("independent repository identity changed")
		}
		if err := validateDeploymentRemotes(ctx, state.record.SourceDirectory, binding); err != nil {
			return result, err
		}
		result.Status = "ready"
		return result, nil
	}
	approval := option(values, "repository-approve")
	if approval == "" && ui != nil {
		visibility := "private"
		if !review.Private {
			visibility = "public"
		}
		fmt.Fprintf(ui.out, "Independent authoring repository: %s\nAccount: @%s\nVisibility: %s\nAction: %s\nSource: %s at %s\nPublishes %d files to %s as a root snapshot; no upstream history or local changes.\n",
			review.Repository, review.Account.Login, visibility, review.Action, review.Application.Repository, review.Application.Commit, len(review.Files), review.Branch)
		for _, warning := range review.Warnings {
			fmt.Fprintln(ui.out, warning)
		}
		for _, file := range review.Files {
			fmt.Fprintf(ui.out, "  %s (%d bytes, %s)\n", file.Path, file.Bytes, file.Mode)
		}
		for _, worktree := range review.Worktrees {
			fmt.Fprintf(ui.out, "Shared remote changes affect: %q\n", worktree)
		}
		yes, err := ui.confirm(ctx, fmt.Sprintf("Create/use %s repository %s, disable Actions, and publish this source snapshot?", visibility, repoSlug(review.Repository)))
		if errors.Is(err, errCreateCancelled) {
			return result, nil
		}
		if err != nil {
			return result, err
		}
		if !yes {
			if binding == nil {
				choice.Mode = "skip"
				if err := saveAuthoringChoice(state, choice); err != nil {
					return result, err
				}
				result.Status = "skipped"
			}
			return result, nil
		}
		approval = result.ReviewSHA256
	}
	if approval == "" {
		return result, nil
	}
	if approval != result.ReviewSHA256 {
		return result, fmt.Errorf("repository approval does not match the current source-publication review")
	}
	lock := filepath.Join(state.record.CommonDirectory, "radpack-fork.lock")
	file, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, fmt.Errorf("repository setup is active or interrupted; inspect the Git-local radpack-fork.lock before retrying")
	}
	if err := file.Close(); err != nil {
		return result, err
	}
	defer func() { resultErr = errors.Join(resultErr, os.Remove(lock)) }()
	fresh, err := readDeploymentBinding(ctx, state.record.SourceDirectory)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(fresh, binding) {
		return result, fmt.Errorf("repository setup journal changed; review again")
	}
	freshChoice, err := readAuthoringChoice(state)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(freshChoice, choice) {
		return result, fmt.Errorf("repository choice changed during review; resume again")
	}
	if binding == nil {
		freshReview, err := planRepository(ctx, state, choice, api)
		if err != nil {
			return result, err
		}
		if !reflect.DeepEqual(freshReview, review) {
			return result, fmt.Errorf("source-publication review changed; approve again")
		}
	}
	if _, err := applyRepository(ctx, state, review, binding, api); err != nil {
		return result, err
	}
	result.Status = "ready"
	result.Message = "Pinned source snapshot published; this checkout now uses radpack-authoring tracking origin/radpack-authoring. Previous branches and uncommitted files are preserved. Actions remain disabled. Reopen the SAME Radius instance explicitly for this repository and run fresh readiness. No environment or cloud deployment was created."
	return result, nil
}
