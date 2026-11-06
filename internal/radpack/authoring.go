package radpack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const authoringStatePath = "radpack-authoring"

// This record is Git-local metadata, never an application file. It travels
// across linked worktrees but is not published by Git or captured in a pack.
type authoringRecord struct {
	FormatVersion   int               `json:"formatVersion"`
	Upstream        Upstream          `json:"upstream"`
	SourceDirectory string            `json:"sourceDirectory"`
	CommonDirectory string            `json:"commonDirectory"`
	Output          string            `json:"output"`
	Name            string            `json:"name"`
	RadiusVersion   *string           `json:"radiusVersion"`
	Include         []string          `json:"include"`
	Integration     IntegrationAssets `json:"integration"`
	CreatedAt       string            `json:"createdAt"`
}

type authoringSaved struct {
	FormatVersion      int    `json:"formatVersion"`
	Output             string `json:"output"`
	PackManifestSHA256 string `json:"packManifestSha256"`
	ReviewSHA256       string `json:"reviewSha256"`
	WorktreeDirectory  string `json:"worktreeDirectory"`
}

type authoringState struct {
	directory string
	record    authoringRecord
	saved     *authoringSaved
}

type AuthoringReport struct {
	ReportVersion   int                  `json:"reportVersion"`
	Command         string               `json:"command"`
	Status          string               `json:"status"`
	Stage           string               `json:"stage"`
	Source          string               `json:"source,omitempty"`
	Worktree        string               `json:"worktree,omitempty"`
	Application     *Upstream            `json:"application,omitempty"`
	Output          string               `json:"output,omitempty"`
	ResumeArgs      []string             `json:"resumeArgs,omitempty"`
	Worktrees       []string             `json:"worktrees,omitempty"`
	PackSHA256      string               `json:"packManifestSha256,omitempty"`
	Diagnostics     []ReadinessIssue     `json:"diagnostics"`
	Publication     string               `json:"publication"`
	Deployment      string               `json:"deploymentVerification"`
	Credentials     string               `json:"credentials"`
	RepositorySetup *authoringDeployment `json:"repositorySetup,omitempty"`
}

func option(values map[string][]string, key string) string {
	if len(values[key]) > 0 {
		return values[key][0]
	}
	return ""
}

func validAuthoringOptions(values map[string][]string) error {
	if option(values, "approve") != "" && option(values, "dry-run") != "" {
		return fmt.Errorf("use --dry-run or --approve, not both")
	}
	for _, key := range []string{"include", "recipe", "recipe-pack"} {
		for _, file := range values[key] {
			if err := safePath(file); err != nil {
				return err
			}
		}
	}
	if name := option(values, "name"); name != "" && !namePattern.MatchString(name) {
		return fmt.Errorf("invalid pack name")
	}
	if _, ok := values["radius-version"]; ok && releaseVersion(option(values, "radius-version")) == "" {
		return fmt.Errorf("--radius-version must be an exact release version")
	}
	return nil
}

func startAuthoring(ctx context.Context, target string, values map[string][]string) (*authoringState, error) {
	if err := validAuthoringOptions(values); err != nil {
		return nil, err
	}
	if err := validateCreateForkOptions(values, true); err != nil {
		return nil, err
	}
	repository, err := repositoryURL(target)
	if err != nil {
		return nil, err
	}
	name := option(values, "name")
	if name == "" {
		name = regexpName(strings.ToLower(strings.TrimSuffix(path.Base(repository), ".git")))
	}
	if !namePattern.MatchString(name) {
		return nil, fmt.Errorf("invalid pack name; supply --name")
	}
	destination := option(values, "directory")
	if destination == "" {
		destination = strings.TrimSuffix(path.Base(repository), ".git")
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return nil, err
	}
	// Refuse existing directories before any network request, including symlinks.
	if _, err := os.Lstat(destination); err == nil {
		return nil, fmt.Errorf("destination already exists: %s. Use create --resume for a previous authoring checkout, or choose another --directory", destination)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	output := option(values, "output")
	if output == "" {
		output = filepath.Join("contrib", name)
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return nil, err
	}
	if destination == output {
		return nil, fmt.Errorf("source directory and pack output must be different")
	}
	base := option(values, "base")
	if base != "" && !commitPattern.MatchString(base) {
		return nil, fmt.Errorf("--base must be a full 40-character published commit SHA")
	}
	if base == "" {
		base, err = resolveRevision(ctx, repository, "HEAD")
		if err != nil {
			return nil, err
		}
	}
	var version *string
	if _, ok := values["radius-version"]; ok {
		v := releaseVersion(option(values, "radius-version"))
		version = &v
	}
	var state *authoringState
	_, err = withNewDirectory(destination, func(root string) error {
		if err := cloneRevision(ctx, root, repository, base); err != nil {
			return err
		}
		source, err := canonicalDirectory(root)
		if err != nil {
			return err
		}
		common, err := commonDirectory(ctx, source)
		if err != nil {
			return err
		}
		local := filepath.Join(common, authoringStatePath)
		if err := os.Mkdir(local, 0700); err != nil {
			return err
		}
		record := authoringRecord{
			FormatVersion: 1, Upstream: Upstream{repository, base},
			SourceDirectory: source, CommonDirectory: common, Output: output,
			Name: name, RadiusVersion: version,
			Include: append([]string{}, values["include"]...),
			Integration: IntegrationAssets{
				Recipes:     append([]string{}, values["recipe"]...),
				RecipePacks: append([]string{}, values["recipe-pack"]...),
			},
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if err := savePrivateJSON(local, "request.json", record); err != nil {
			return err
		}
		choice := authoringDeploymentChoice{FormatVersion: 1, Mode: "independent", Name: option(values, "fork-name"), Public: option(values, "public") != ""}
		if option(values, "repository") != "" {
			choice.Repository, err = repositoryURL(option(values, "repository"))
			if err != nil {
				return err
			}
		}
		if option(values, "fork") != "" || choice.Name != "" {
			choice.Mode = "fork"
		}
		if option(values, "use-source") != "" {
			choice.Mode = "source"
		}
		if option(values, "no-fork") != "" {
			choice.Mode = "skip"
		}
		if err := savePrivateJSON(local, "deployment-choice.json", choice); err != nil {
			return err
		}
		state = &authoringState{directory: local, record: record}
		return nil
	})
	return state, err
}

func loadAuthoring(ctx context.Context, directory string) (*authoringState, error) {
	root, err := repositoryRoot(ctx, directory)
	if err != nil {
		return nil, err
	}
	common, err := commonDirectory(ctx, root)
	if err != nil {
		return nil, err
	}
	request, err := readRegular(common, authoringStatePath+"/request.json")
	if err != nil {
		return nil, err
	}
	if request == nil {
		return nil, nil
	}
	var record authoringRecord
	if err := decodeStrict(request.Bytes, &record); err != nil {
		return nil, err
	}
	repository, err := repositoryURL(record.Upstream.Repository)
	if err != nil || repository != record.Upstream.Repository || record.FormatVersion != 1 ||
		!commitPattern.MatchString(record.Upstream.Commit) || !namePattern.MatchString(record.Name) ||
		record.CommonDirectory != common || !filepath.IsAbs(record.SourceDirectory) || !filepath.IsAbs(record.Output) {
		return nil, fmt.Errorf("invalid or relocated authoring record; no files changed")
	}
	if record.RadiusVersion != nil && releaseVersion(*record.RadiusVersion) == "" {
		return nil, fmt.Errorf("invalid authoring Radius version")
	}
	for _, file := range append(append(append([]string{}, record.Include...), record.Integration.Recipes...), record.Integration.RecipePacks...) {
		if err := safePath(file); err != nil {
			return nil, err
		}
	}
	source, err := repositoryRoot(ctx, record.SourceDirectory)
	if err != nil {
		return nil, fmt.Errorf("recorded authoring source is unavailable: %w", err)
	}
	sourceCommon, err := commonDirectory(ctx, source)
	if err != nil {
		return nil, err
	}
	if source != record.SourceDirectory || sourceCommon != common {
		return nil, fmt.Errorf("authoring clone identity changed")
	}
	baseline, err := gitText(ctx, root, "rev-parse", "--verify", record.Upstream.Commit+"^{commit}")
	if err != nil || baseline != record.Upstream.Commit {
		return nil, fmt.Errorf("recorded authoring baseline is unavailable")
	}
	state := &authoringState{directory: filepath.Join(common, authoringStatePath), record: record}
	saved, err := readRegular(common, authoringStatePath+"/saved.json")
	if err != nil {
		return nil, err
	}
	if saved != nil {
		var completion authoringSaved
		if err := decodeStrict(saved.Bytes, &completion); err != nil {
			return nil, err
		}
		if completion.FormatVersion != 1 || !filepath.IsAbs(completion.Output) ||
			!filepath.IsAbs(completion.WorktreeDirectory) || !hashPattern.MatchString(completion.PackManifestSHA256) ||
			!hashPattern.MatchString(completion.ReviewSHA256) {
			return nil, fmt.Errorf("invalid saved authoring record")
		}
		state.saved = &completion
	}
	return state, nil
}

func authoringWorktrees(ctx context.Context, state *authoringState) ([]string, error) {
	output, err := git(ctx, state.record.SourceDirectory, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	result := []string{}
	for _, field := range splitNull(output) {
		if !strings.HasPrefix(field, "worktree ") {
			continue
		}
		directory, err := canonicalDirectory(strings.TrimPrefix(field, "worktree "))
		if os.IsNotExist(err) {
			continue // Prunable worktrees are not eligible selections.
		}
		if err != nil {
			return nil, err
		}
		common, err := commonDirectory(ctx, directory)
		if err != nil {
			return nil, err
		}
		if common != state.record.CommonDirectory {
			return nil, fmt.Errorf("worktree identity differs from authoring source")
		}
		result = append(result, directory)
	}
	return uniqueSorted(result), nil
}

func hasAuthoringModel(root string) (bool, error) {
	for _, file := range []string{".radius/app.bicep", "app.bicep"} {
		model, err := readRegular(root, file)
		if err != nil {
			return false, err
		}
		if model != nil {
			return true, nil
		}
	}
	return false, nil
}

func authoringResumeArgs(state *authoringState, source string) []string {
	result := []string{"create", "--resume", state.record.SourceDirectory}
	if source != "" && source != state.record.SourceDirectory {
		result = append(result, "--source", source)
	}
	return result
}

// A waiting workflow is durable before any prompt. Nonterminal/JSON callers
// never wait on stdin and receive one structured result per invocation.
func authoringCommand(ctx context.Context, target string, values map[string][]string, out io.Writer, ui *createUI) (resultErr error) {
	return authoringWorkflow(ctx, target, values, out, ui, true)
}

func authoringWorkflow(ctx context.Context, target string, values map[string][]string, out io.Writer, ui *createUI, allowPause bool) (resultErr error) {
	return authoringWorkflowWithAPI(ctx, target, values, out, ui, allowPause, callGitHub)
}

func authoringWorkflowWithAPI(ctx context.Context, target string, values map[string][]string, out io.Writer, ui *createUI, allowPause bool, api githubAPI) (resultErr error) {
	get := func(key string) string { return option(values, key) }
	machine := get("json") != ""
	automatic := machine || get("dry-run") != "" || get("approve") != "" || get("status") != "" || get("fork-approve") != "" || get("repository-approve") != ""
	if automatic {
		ui = nil
	}
	report := AuthoringReport{ReportVersion: 1, Command: "create", Status: "blocked", Stage: "authoring",
		Diagnostics: []ReadinessIssue{}, Publication: "not-performed", Deployment: "not-performed", Credentials: "not-inspected"}
	delegated := false
	emit := func() error {
		if machine {
			data, err := jsonBytes(report)
			if err != nil {
				return err
			}
			_, err = out.Write(data)
			return err
		}
		fmt.Fprintf(out, "Authoring status: %s\n", report.Status)
		if report.Source != "" {
			fmt.Fprintf(out, "Source checkout: %s\nStarting revision: %s\nPack output: %s\n", report.Source, report.Application.Commit, report.Output)
		}
		if report.Worktree != "" && report.Worktree != report.Source {
			fmt.Fprintf(out, "Selected worktree: %s\n", report.Worktree)
		}
		if report.RepositorySetup != nil {
			fmt.Fprintf(out, "Deployment repository setup: %s\n", report.RepositorySetup.Status)
			if report.RepositorySetup.Repository != "" {
				fmt.Fprintf(out, "Deployment repository: %s\n", report.RepositorySetup.Repository)
			}
			if report.RepositorySetup.ReviewSHA256 != "" {
				fmt.Fprintf(out, "Repository setup review SHA: %s\n", report.RepositorySetup.ReviewSHA256)
			}
			if report.RepositorySetup.Message != "" {
				fmt.Fprintln(out, report.RepositorySetup.Message)
			}
		}
		if len(report.Worktrees) > 1 {
			fmt.Fprintln(out, "Linked checkouts:")
			for i, root := range report.Worktrees {
				fmt.Fprintf(out, "  %d. %s\n", i+1, root)
			}
		}
		for _, diagnostic := range report.Diagnostics {
			fmt.Fprintf(out, "%s %s\n", diagnostic.Message, diagnostic.NextAction)
		}
		if len(report.ResumeArgs) > 0 {
			fmt.Fprintf(out, "Resume with: radpack create --resume %q", report.Source)
			if report.Worktree != "" && report.Worktree != report.Source {
				fmt.Fprintf(out, " --source %q", report.Worktree)
			}
			fmt.Fprintln(out)
		}
		return nil
	}
	defer func() {
		if delegated {
			return
		}
		if resultErr != nil {
			report.Status = "blocked"
			code := "authoring-" + report.Stage + "-failed"
			report.Diagnostics = append(report.Diagnostics, ReadinessIssue{Code: code, Phase: report.Stage,
				Message: resultErr.Error(), NextAction: "Inspect the source and local authoring state. Existing prepared files are preserved; do not reclone over them."})
		}
		resultErr = errors.Join(resultErr, emit())
	}()
	if err := validAuthoringOptions(values); err != nil {
		return err
	}
	var state *authoringState
	var err error
	if target != "" {
		report.Stage = "clone"
		state, err = startAuthoring(ctx, target, values)
	} else {
		report.Stage = "resume"
		state, err = loadAuthoring(ctx, get("resume"))
		if err == nil && state == nil {
			err = fmt.Errorf("no Radpack authoring workflow found; use create <GitHub URL> to start one, or create --source for an ordinary prepared checkout")
		}
	}
	if err != nil {
		return err
	}
	report.Source = state.record.SourceDirectory
	report.Application = &state.record.Upstream
	report.Output = state.record.Output
	report.ResumeArgs = authoringResumeArgs(state, "")

	// Saving is final for this workflow. Resume checks the output rather than
	// recreating it; publish is a separate, unimplemented operation.
	if state.saved != nil {
		report.Stage = "save"
		report.Output = state.saved.Output
		report.Worktree = state.saved.WorktreeDirectory
		if get("output") != "" {
			requested, err := filepath.Abs(get("output"))
			if err != nil {
				return err
			}
			if requested != report.Output {
				return fmt.Errorf("workflow already saved; use ordinary create --source for a new pack")
			}
		}
		pack, err := LoadPack(state.saved.Output)
		if err != nil {
			return fmt.Errorf("saved pack is unavailable or modified; do not recreate it blindly: %w", err)
		}
		if pack.Digest != state.saved.PackManifestSHA256 {
			return fmt.Errorf("saved pack differs from its authoring checkpoint")
		}
		report.Status = "saved"
		report.PackSHA256 = pack.Digest
		report.ResumeArgs = nil
		report.Diagnostics = append(report.Diagnostics, ReadinessIssue{Code: "pack-already-saved", Phase: "save",
			Message:    "The local pack is saved. No pack publication or cloud deployment has been performed.",
			NextAction: "Use radpack inspect/report or install --no-open to review the saved pack. Publication is a separate action."})
		return nil
	}
	for key, expected := range map[string]string{"base": state.record.Upstream.Commit, "upstream": state.record.Upstream.Repository} {
		if requested := get(key); requested != "" {
			if key == "upstream" {
				requested, err = repositoryURL(requested)
				if err != nil {
					return err
				}
			}
			if requested != expected {
				return fmt.Errorf("resume preserves the recorded %s; use ordinary create --source to choose another baseline", key)
			}
		}
	}
	report.Stage = "deployment-repository"
	setup, err := prepareAuthoringDeployment(ctx, state, values, ui, api)
	report.RepositorySetup = &setup
	if err != nil {
		return err
	}
	if setup.Status != "ready" && setup.Status != "skipped" {
		report.Status = setup.Status
		report.Diagnostics = append(report.Diagnostics, ReadinessIssue{Code: setup.Status, Phase: report.Stage,
			Message: setup.Message, NextAction: setup.NextAction})
		return nil
	}
	if setup.Status == "skipped" && !machine {
		fmt.Fprintln(out, "Continuing locally without repository setup. GitHub-based Radius deployment still needs a repository you administer.")
	}
	roots, err := authoringWorktrees(ctx, state)
	if err != nil {
		return err
	}
	report.Worktrees = roots
	selected := get("source")
	// Resuming with the worktree path itself is an explicit choice.
	if selected == "" && target == "" {
		resumed, err := repositoryRoot(ctx, get("resume"))
		if err != nil {
			return err
		}
		if resumed != state.record.SourceDirectory {
			selected = resumed
		}
	}
	if selected != "" {
		selected, err = repositoryRoot(ctx, selected)
		if err != nil {
			return err
		}
		found := false
		for _, root := range roots {
			if root == selected {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("--source must be the recorded clone or one of its linked worktrees")
		}
	} else if len(roots) == 1 {
		selected = roots[0]
	} else {
		report.Status = "awaiting-worktree-selection"
		report.Stage = "workspace"
		report.Diagnostics = append(report.Diagnostics, ReadinessIssue{Code: "worktree-selection-required", Phase: "workspace",
			Message:    "Multiple linked checkouts exist; select the one you Radified.",
			NextAction: "Resume with --source <linked-worktree>, or pass the worktree path directly to --resume."})
		if ui == nil {
			return nil
		}
		if err := emit(); err != nil {
			return err
		}
		answer, err := ui.line(ctx, "Choose the checkout number to review (Enter pauses): ")
		if errors.Is(err, errCreateCancelled) || (err == nil && answer == "") {
			return nil
		}
		if err != nil {
			return err
		}
		chosen, err := selection(answer, roots)
		if err != nil || len(chosen) != 1 {
			return fmt.Errorf("choose exactly one linked checkout; no pack written")
		}
		selected = chosen[0]
		report.Diagnostics = []ReadinessIssue{}
	}
	report.Worktree = selected
	report.ResumeArgs = authoringResumeArgs(state, selected)
	present, err := hasAuthoringModel(selected)
	if err != nil {
		return err
	}
	// Always pause a newly cloned application, even if it already has a model.
	// Completion is an author decision, not inferred from a filename.
	if target != "" || !present || get("repository-approve") != "" {
		report.Status = "awaiting-radification"
		report.Stage = "model"
		report.Diagnostics = append(report.Diagnostics, ReadinessIssue{Code: "radification-required", Phase: "model",
			Message:    "Prepare the application with Radius in this checkout or a linked Copilot App worktree. No modeling, environment setup, or deployment has run.",
			NextAction: "Use the selected deployment repository when opening a Radius deployment session. When ready, resume this workflow to review and save a pack. Graph generation alone is not deployment verification."})
		if get("approve") != "" && !present {
			return fmt.Errorf("no application model found; resume after Radifying the application")
		}
		if ui == nil || !allowPause {
			return nil
		}
		if err := emit(); err != nil {
			return err
		}
		answer, err := ui.line(ctx, "Go Radify your app. When finished, press Enter to review; type q to pause (you can close this terminal and resume later): ")
		if errors.Is(err, errCreateCancelled) || (err == nil && (strings.EqualFold(answer, "q") || strings.EqualFold(answer, "quit"))) {
			return nil
		}
		if err != nil {
			return err
		}
		if answer != "" {
			return fmt.Errorf("press Enter to continue or q to pause; no pack written")
		}
		// Re-enter through the recorded state so newly created worktrees must be
		// selected rather than exporting the original unmodified clone.
		resumed := cloneOptions(values)
		delete(resumed, "directory")
		for _, key := range []string{"fork", "no-fork", "fork-name", "fork-approve", "use-source", "independent", "repository", "public", "private", "repository-approve"} {
			delete(resumed, key)
		}
		resumed["resume"] = []string{state.record.SourceDirectory}
		delegated = true
		return authoringWorkflowWithAPI(ctx, "", resumed, out, ui, false, api)
	}
	report.Status = "ready-for-review"
	report.Stage = "review"
	if get("status") != "" {
		return nil
	}
	resolved := cloneOptions(values)
	for _, key := range []string{"resume", "directory", "status", "fork", "no-fork", "fork-name", "fork-approve", "use-source", "independent", "repository", "public", "private", "repository-approve"} {
		delete(resolved, key)
	}
	resolved["source"] = []string{selected}
	resolved["upstream"] = []string{state.record.Upstream.Repository}
	resolved["base"] = []string{state.record.Upstream.Commit}
	for key, fallback := range map[string]string{"output": state.record.Output, "name": state.record.Name} {
		if option(resolved, key) == "" {
			resolved[key] = []string{fallback}
		}
	}
	if _, ok := resolved["radius-version"]; !ok && state.record.RadiusVersion != nil {
		resolved["radius-version"] = []string{*state.record.RadiusVersion}
	}
	for key, fallback := range map[string][]string{"include": state.record.Include,
		"recipe": state.record.Integration.Recipes, "recipe-pack": state.record.Integration.RecipePacks} {
		resolved[key] = uniqueSorted(append(append([]string{}, fallback...), resolved[key]...))
	}
	// Serialize review/save invocations so two resumed terminals cannot both
	// create output and race to mark this workflow saved.
	lock := filepath.Join(state.directory, "review.lock")
	claim, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("another authoring review is active or was interrupted; inspect %s before retrying", lock)
	}
	if err := claim.Close(); err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(lock); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("could not release authoring review lock: %w", err))
		}
	}()
	// Another process may have completed between our initial read and the lock.
	fresh, err := loadAuthoring(ctx, selected)
	if err != nil {
		return err
	}
	if fresh == nil || !reflect.DeepEqual(fresh.record, state.record) || fresh.saved != nil {
		return fmt.Errorf("authoring workflow changed; resume again to inspect its current state")
	}
	save := func(output, reviewSHA string) error {
		current, err := loadAuthoring(ctx, selected)
		if err != nil {
			return err
		}
		if current == nil || !reflect.DeepEqual(current.record, state.record) || current.saved != nil {
			return fmt.Errorf("authoring state changed during review")
		}
		pack, err := LoadPack(output)
		if err != nil {
			return err
		}
		return savePrivateJSON(state.directory, "saved.json", authoringSaved{1, output, pack.Digest, reviewSHA, selected})
	}
	delegated = true
	return createCommandWithSave(ctx, resolved, out, ui, save)
}

func cloneOptions(values map[string][]string) map[string][]string {
	copy := make(map[string][]string, len(values))
	for key, value := range values {
		copy[key] = append([]string{}, value...)
	}
	return copy
}
