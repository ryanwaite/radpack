package radpack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

type authoringDeploymentChoice struct {
	FormatVersion int    `json:"formatVersion"`
	Mode          string `json:"mode"`
	Name          string `json:"name"`
}

type authoringDeployment struct {
	Status       string      `json:"status"`
	Repository   string      `json:"repository,omitempty"`
	ReviewSHA256 string      `json:"reviewSha256,omitempty"`
	Review       *ForkReview `json:"review,omitempty"`
	Message      string      `json:"message,omitempty"`
	NextAction   string      `json:"nextAction,omitempty"`
}

func validateCreateForkOptions(values map[string][]string, starting bool) error {
	has := func(key string) bool { _, ok := values[key]; return ok }
	if has("fork") && has("no-fork") {
		return fmt.Errorf("use --fork or --no-fork, not both")
	}
	if has("use-source") && (has("fork") || has("no-fork") || has("fork-name") || has("fork-approve")) {
		return fmt.Errorf("--use-source cannot be combined with fork options or --no-fork")
	}
	if has("no-fork") && (has("fork-name") || has("fork-approve")) {
		return fmt.Errorf("--no-fork cannot include fork name or approval")
	}
	if has("status") && (has("fork") || has("no-fork") || has("fork-name") || has("use-source")) {
		return fmt.Errorf("--status is read-only; choose fork options on a normal resume")
	}
	if has("fork-name") && (!match(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`, option(values, "fork-name")) || strings.HasSuffix(option(values, "fork-name"), ".git")) {
		return fmt.Errorf("--fork-name must be a GitHub repository name")
	}
	if has("fork-approve") {
		if starting {
			return fmt.Errorf("start authoring first, then use --resume --fork-approve after reviewing the fork")
		}
		if !hashPattern.MatchString(option(values, "fork-approve")) {
			return fmt.Errorf("--fork-approve requires the exact fork review SHA")
		}
		if has("approve") || has("dry-run") || has("status") {
			return fmt.Errorf("fork approval must be separate from pack-save approval, dry-run, and status")
		}
	}
	return nil
}

func readAuthoringChoice(state *authoringState) (authoringDeploymentChoice, error) {
	file, err := readRegular(state.directory, "deployment-choice.json")
	if err != nil {
		return authoringDeploymentChoice{}, err
	}
	if file == nil {
		return authoringDeploymentChoice{1, "fork", ""}, nil
	}
	var choice authoringDeploymentChoice
	if err := decodeStrict(file.Bytes, &choice); err != nil {
		return choice, err
	}
	if choice.FormatVersion != 1 || (choice.Mode != "auto" && choice.Mode != "fork" && choice.Mode != "skip" && choice.Mode != "source") ||
		(choice.Name != "" && (!match(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`, choice.Name) || strings.HasSuffix(choice.Name, ".git"))) {
		return choice, fmt.Errorf("invalid authoring deployment choice")
	}
	return choice, nil
}

func saveAuthoringChoice(state *authoringState, choice authoringDeploymentChoice) (err error) {
	if _, err := readRegular(state.directory, "deployment-choice.json"); err != nil {
		return err
	}
	data, err := jsonBytes(choice)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(state.directory, ".choice-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer func() {
		if e := os.Remove(name); e != nil && !os.IsNotExist(e) {
			err = errors.Join(err, e)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(state.directory, "deployment-choice.json"))
}

func forkReportFor(ctx context.Context, state *authoringState, choice authoringDeploymentChoice, approval string, api githubAPI) (ForkReport, error) {
	values := map[string][]string{"directory": {state.record.SourceDirectory}, "json": {"true"}}
	if choice.Name != "" {
		values["name"] = []string{choice.Name}
	}
	if approval != "" {
		values["approve"] = []string{approval}
	}
	var out strings.Builder
	err := forkCommand(ctx, values, &out, nil, api)
	var result ForkReport
	if e := json.Unmarshal([]byte(out.String()), &result); e != nil {
		return result, errors.Join(err, fmt.Errorf("fork setup did not return a valid report"))
	}
	return result, err
}

func prepareAuthoringDeployment(ctx context.Context, state *authoringState, values map[string][]string, ui *createUI, api githubAPI) (authoringDeployment, error) {
	waiting := authoringDeployment{Status: "awaiting-deployment-choice",
		Message:    "Choose a repository you administer for Radius deployment, or continue with local-only authoring.",
		NextAction: "Resume interactively, or use --fork to review a personal fork and --no-fork to skip. Fork approval is separate from saving a pack."}
	if err := validateCreateForkOptions(values, false); err != nil {
		return waiting, err
	}
	choice, err := readAuthoringChoice(state)
	if err != nil {
		return waiting, err
	}
	binding, err := readDeploymentBinding(ctx, state.record.SourceDirectory)
	if err != nil {
		return waiting, err
	}
	// Status is read-only, including no account/network probing or persistence.
	if option(values, "status") != "" {
		if binding != nil {
			waiting.Repository = binding.Repository
			waiting.ReviewSHA256 = binding.ReviewSHA256
			if binding.Status == "ready" {
				if err := validateDeploymentRemotes(ctx, state.record.SourceDirectory, binding); err != nil {
					return waiting, err
				}
				waiting.Status = "ready"
				return waiting, nil
			}
			waiting.Status = "awaiting-fork-approval"
			waiting.NextAction = "Resume with --fork-approve and the recorded fork approval to reconcile pending setup."
			return waiting, nil
		}
		if choice.Mode == "skip" {
			return authoringDeployment{Status: "skipped"}, nil
		}
		return waiting, nil
	}
	next := choice
	if option(values, "fork") != "" || option(values, "fork-name") != "" || option(values, "fork-approve") != "" {
		next.Mode = "fork"
	}
	if option(values, "no-fork") != "" {
		next.Mode = "skip"
	}
	if option(values, "use-source") != "" {
		next.Mode = "source"
		next.Name = ""
	}
	if _, ok := values["fork-name"]; ok {
		next.Name = option(values, "fork-name")
	}
	if binding != nil && option(values, "no-fork") != "" {
		return waiting, fmt.Errorf("fork setup is already recorded; --no-fork cannot undo or abandon a possibly created fork. Resume radpack fork to inspect it")
	}
	if binding != nil && option(values, "use-source") != "" {
		return waiting, fmt.Errorf("fork setup is already recorded; --use-source cannot undo or abandon a possibly created fork. Resume radpack fork to inspect it")
	}
	if binding != nil {
		next.Mode = "fork"
	}
	if !reflect.DeepEqual(next, choice) {
		if err := saveAuthoringChoice(state, next); err != nil {
			return waiting, err
		}
		choice = next
	}
	if choice.Mode == "skip" {
		return authoringDeployment{Status: "skipped"}, nil
	}

	// Keep the previous automatic policy for workflows that already recorded it.
	if binding == nil && (choice.Mode == "auto" || choice.Mode == "source") {
		remotes, err := readDeploymentRemotes(ctx, state.record.SourceDirectory)
		if err != nil {
			return waiting, err
		}
		origin, err := repositoryURL(remotes.Origin)
		if err != nil {
			return waiting, err
		}
		if !strings.EqualFold(origin, state.record.Upstream.Repository) {
			return waiting, fmt.Errorf("origin differs from the recorded application without an approved deployment binding; resolve repository context or use --no-fork for local-only work")
		}
		// GET the selected source, never its parent. Lack of evidence (including
		// SAML refusal) does not prove this account needs a fork.
		person, err := readGitHubUser(ctx, api)
		if err != nil {
			waiting.Status = "awaiting-github-access"
			waiting.Message = err.Error()
			waiting.NextAction = "Fix gh authentication and retry, use --fork to explicitly choose the fork path, or --no-fork to continue locally."
			return waiting, nil
		}
		source, err := readGitHubFork(ctx, api, state.record.Upstream.Repository)
		if err != nil || source == nil {
			waiting.Status = "awaiting-github-access"
			waiting.Message = "Could not verify repository administration with the selected GitHub account."
			if err != nil {
				waiting.Message = err.Error()
			}
			waiting.NextAction = "Resolve source access, explicitly choose --fork, or use --no-fork to continue locally. Missing access is not proof of insufficient permissions."
			return waiting, nil
		}
		if !strings.EqualFold(source.FullName, repoSlug(state.record.Upstream.Repository)) {
			return waiting, fmt.Errorf("GitHub source identity differs from the recorded application")
		}
		if source.Permissions.Admin && !source.Archived && !source.Disabled {
			if ui != nil {
				fmt.Fprintf(ui.out, "@%s can administer %s; no personal fork is needed. Radius must still check Actions, packages, and cloud access.\n", person.Login, source.FullName)
			}
			return authoringDeployment{Status: "ready", Repository: state.record.Upstream.Repository,
				Message: "Source administration verified; no fork needed. This is not full Radius readiness."}, nil
		}
		if choice.Mode == "source" {
			waiting.Status = "awaiting-github-access"
			waiting.Message = "The selected source repository must be active and administrable by your GitHub account."
			waiting.NextAction = "Resolve source permissions, use --fork to review a personal fork, or --no-fork to continue locally."
			return waiting, nil
		}
	}

	result, err := forkReportFor(ctx, state, choice, "", api)
	waiting.Repository = result.Repository
	waiting.ReviewSHA256 = result.ReviewSHA256
	waiting.Review = result.Review
	if err != nil {
		return waiting, err
	}
	if result.Status == "ready" {
		return authoringDeployment{Status: "ready", Repository: result.Repository, Message: result.NextAction}, nil
	}
	waiting.Status = "awaiting-fork-approval"
	waiting.Message = "Review the public personal fork and shared remote changes before approving. No fork was created by this preview."
	waiting.NextAction = "Resume with --fork-approve <reviewSha256> to approve this fork only, or --no-fork to skip when no fork operation has started."
	approval := option(values, "fork-approve")
	if approval == "" && ui != nil {
		fmt.Fprintln(ui.out, "Radius needs a GitHub repository you administer for environments and workflows.")
		if result.Review != nil {
			r := result.Review
			fmt.Fprintf(ui.out, "Application source: %s\nPersonal fork: %s\nGitHub account: @%s\nAction: %s\nVisibility: public\n%s\n", r.Application.Repository, r.Repository, r.Account.Login, r.Action, r.Warning)
			for _, worktree := range r.Worktrees {
				fmt.Fprintf(ui.out, "  Shared remote change affects: %q\n", worktree)
			}
		} else {
			fmt.Fprintf(ui.out, "Pending personal fork: %s\nThe original operation may have created it; resume only after reviewing that destination.\n", result.Repository)
		}
		yes, err := ui.confirm(ctx, "Create or reuse this personal fork for deployment?")
		if errors.Is(err, errCreateCancelled) {
			return waiting, nil
		}
		if err != nil {
			return waiting, err
		}
		if !yes {
			if binding != nil {
				return waiting, nil
			}
			choice.Mode = "skip"
			if err := saveAuthoringChoice(state, choice); err != nil {
				return waiting, err
			}
			return authoringDeployment{Status: "skipped"}, nil
		}
		approval = result.ReviewSHA256
	}
	if approval == "" {
		return waiting, nil
	}
	// Reuse the fork command's fresh review, journal, identity checks and lock.
	result, err = forkReportFor(ctx, state, choice, approval, api)
	waiting.Repository = result.Repository
	waiting.ReviewSHA256 = result.ReviewSHA256
	if err != nil {
		return waiting, err
	}
	if result.Status != "ready" {
		return waiting, fmt.Errorf("fork setup did not confirm ready")
	}
	if ui != nil {
		fmt.Fprintf(ui.out, "Personal deployment fork configured: %s\n%s\n", result.Repository, result.NextAction)
	}
	return authoringDeployment{Status: "ready", Repository: result.Repository, Message: result.NextAction}, nil
}
