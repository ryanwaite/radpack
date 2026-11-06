package radpack

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

const help = `Radpack - set up cloud-native apps, explore their architecture, and deploy to your cloud of choice with Radius

  radpack create <github-url|owner/repo> [--directory <new-checkout>] [--output <pack-directory>] [--json]
                 [--independent | --fork | --no-fork | --use-source]
                 [--repository <owner/repo>] [--public | --private] [--fork-name <name>]
  radpack create --resume <checkout> --repository-approve <publication-review-sha> [--json]
  radpack create --resume <checkout> --fork-approve <fork-review-sha> [--json]
  radpack create --resume <authoring-checkout> [--source <linked-worktree>] [--status] [--json]
  radpack create [--base <full-sha>] [--source <repo>] [--output <pack-directory>]
                 [--upstream <owner/repo>] [--name <name>] [--include <file>]...
                 [--recipe <file>]... [--recipe-pack <file>]...
                 [--radius-version <version>] [--dry-run | --approve <review-sha>] [--json]
  radpack inspect <pack-directory>
  radpack report <pack-directory>
  radpack report --directory <installed-directory>
  radpack fork [--directory <checkout>] [--name <personal-fork-name>]
               [--dry-run | --approve <review-sha>] [--json]
  radpack install <owner/repo|github-url|pack-directory> [--directory <new-directory>]
                  [--pack <local-directory|github-tree-url> | --pack-path <repo-directory>]
                  [--latest --adapt] [--no-open]
  radpack open --directory <prepared-source-directory>
  radpack handoff status --directory <session-worktree> --json
  radpack handoff apply --directory <session-worktree> --request-id <id> --approve <review-sha> --json
  radpack verify [<pack-directory>] --directory <installed-directory>

Create infers a published source revision and asks before saving in a terminal.
Create with a URL reviews a private independent authoring repository by default.
Explicit approval publishes a pinned source snapshot with Actions disabled.
--public explicitly selects public visibility; --repository selects a destination.
--fork retains optional GitHub fork setup; --no-fork remembers local-only work.
--use-source checks source administration instead. No upstream admin API is
required for independent repositories. No upstream history or local changes are published.
Resume later with --resume; --status inspects authoring progress without saving.
Noninteractive create only previews unless --approve matches the review SHA.
Output defaults to ./contrib/<pack-name>. Existing destinations are never reused.
Install never creates Radius environments, executes pack scripts, or deploys.
Install defaults to the pack's reference commit. --latest --adapt prepares a
Radius authoring handoff, NOT an automatically adapted or deployment-ready app.
Verify checks file reproduction, not Radius compilation or cloud functionality.
Create detects Radius releases from compiler pins and matching origin records.
Install warns about local Radius version mismatches; it does not block or upgrade.
Fork setup asks before creating/reusing a personal GitHub fork and changing
shared remotes. It preserves application provenance and never pushes local code.
Default install prepares a clean clone and opens a Copilot App session draft.
Start the session, then approve applying the pack in Radius. --no-open installs
directly without launching the app. Plugin setup and sign-in remain user actions.
`

// Keep interspersed flags and repeated --include compatible with the original
// CLI without adding a runtime or external command framework dependency.
func parseOptions(command string, args []string) (map[string][]string, []string, error) {
	schemas := map[string]map[string]bool{
		"create":  {"source": false, "base": false, "output": false, "upstream": false, "name": false, "include": false, "recipe": false, "recipe-pack": false, "radius-version": false, "dry-run": true, "approve": false, "json": true, "directory": false, "resume": false, "status": true, "fork": true, "no-fork": true, "use-source": true, "fork-name": false, "fork-approve": false, "independent": true, "repository": false, "public": true, "private": true, "repository-approve": false},
		"inspect": {},
		"report":  {"directory": false},
		"fork":    {"directory": false, "name": false, "dry-run": true, "approve": false, "json": true},
		"install": {"directory": false, "pack": false, "pack-path": false, "latest": true, "adapt": true, "no-open": true},
		"open":    {"directory": false},
		"handoff": {"directory": false, "json": true, "request-id": false, "approve": false},
		"verify":  {"directory": false},
	}
	schema, ok := schemas[command]
	if !ok {
		return nil, nil, fmt.Errorf("unknown command; use radpack --help")
	}
	values := map[string][]string{}
	positions := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positions = append(positions, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			positions = append(positions, arg)
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			return nil, nil, fmt.Errorf("invalid command options; supplied values withheld")
		}
		key, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		boolean, ok := schema[key]
		if !ok {
			return nil, nil, fmt.Errorf("invalid command options; supplied values withheld")
		}
		if boolean {
			if hasValue {
				return nil, nil, fmt.Errorf("boolean flags do not take values")
			}
			value = "true"
		} else if !hasValue {
			i++
			if i >= len(args) || strings.HasPrefix(args[i], "--") {
				return nil, nil, fmt.Errorf("option requires a value; use radpack --help")
			}
			value = args[i]
		}
		if key != "include" && key != "recipe" && key != "recipe-pack" && len(values[key]) > 0 {
			return nil, nil, fmt.Errorf("duplicate option --%s", key)
		}
		values[key] = append(values[key], value)
	}
	return values, positions, nil
}

func Run(ctx context.Context, args []string, out, errOut io.Writer) int {
	err := runWithUI(ctx, args, out, localRadiusVersion, terminalUI(os.Stdin, out))
	if err != nil {
		fmt.Fprintf(errOut, "radpack: %s\n", err)
		return 1
	}
	return 0
}
func run(ctx context.Context, args []string, out io.Writer, probe radiusVersionProbe) error {
	return runWithUI(ctx, args, out, probe, nil)
}

func runWithUI(ctx context.Context, args []string, out io.Writer, probe radiusVersionProbe, ui *createUI) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		_, err := io.WriteString(out, help)
		return err
	}
	command := args[0]
	values, positions, err := parseOptions(command, args[1:])
	if err != nil {
		return err
	}
	get := func(key string) string {
		if len(values[key]) > 0 {
			return values[key][0]
		}
		return ""
	}
	if command == "fork" {
		if len(positions) != 0 {
			return fmt.Errorf("fork takes --directory, not a positional repository")
		}
		return forkCommand(ctx, values, out, ui, callGitHub)
	}
	if command == "create" {
		if err := validateCreateForkOptions(values, len(positions) > 0); err != nil {
			return err
		}
		if len(positions) > 1 {
			return fmt.Errorf("create accepts at most one GitHub repository")
		}
		if len(positions) == 1 {
			for _, key := range []string{"resume", "source", "upstream", "status", "approve", "dry-run"} {
				if _, ok := values[key]; ok {
					return fmt.Errorf("create with a URL cannot use --%s; clone first, then resume to review or save", key)
				}
			}
			return authoringCommand(ctx, positions[0], values, out, ui)
		}
		if _, ok := values["resume"]; ok {
			if get("resume") == "" {
				return fmt.Errorf("--resume requires an authoring checkout path")
			}
			if _, ok := values["directory"]; ok {
				return fmt.Errorf("--directory selects a new URL checkout; use --source to choose a linked worktree on resume")
			}
			if get("status") != "" && (get("approve") != "" || get("dry-run") != "") {
				return fmt.Errorf("--status cannot be combined with save or preview approval options")
			}
			return authoringCommand(ctx, "", values, out, ui)
		}
		if _, ok := values["directory"]; ok {
			return fmt.Errorf("--directory requires a GitHub repository argument")
		}
		if get("status") != "" {
			return fmt.Errorf("--status requires --resume")
		}
		for _, key := range []string{"fork", "no-fork", "fork-name", "fork-approve", "use-source", "independent", "repository", "public", "private", "repository-approve"} {
			if _, ok := values[key]; ok {
				return fmt.Errorf("--%s requires a URL-started or resumed authoring workflow; use radpack fork for an independent checkout", key)
			}
		}
		return createCommand(ctx, values, out, ui)
	}
	if command == "report" {
		if len(positions) > 1 || (len(positions) == 1 && get("directory") != "") ||
			(len(positions) == 0 && get("directory") == "") {
			return fmt.Errorf("report requires a pack directory OR --directory <installed-checkout>")
		}
		packDirectory := ""
		if len(positions) == 1 {
			packDirectory = positions[0]
		}
		result, err := ReportPack(ctx, packDirectory, get("directory"))
		if err != nil {
			return err
		}
		data, err := jsonBytes(result)
		if err != nil {
			return err
		}
		_, err = out.Write(data)
		return err
	}
	if command == "handoff" {
		if len(positions) != 1 || get("directory") == "" || get("json") == "" {
			return fmt.Errorf("handoff requires status or apply, --directory, and --json")
		}
		var status HandoffStatus
		var err error
		switch positions[0] {
		case "status":
			if get("request-id") != "" || get("approve") != "" {
				return fmt.Errorf("status does not accept mutation options")
			}
			status, err = HandoffStatusFor(ctx, get("directory"))
		case "apply":
			if get("request-id") == "" || get("approve") == "" {
				return fmt.Errorf("apply requires --request-id and --approve after user consent")
			}
			status, err = ApplyHandoff(ctx, get("directory"), get("request-id"), get("approve"))
		default:
			return fmt.Errorf("unknown handoff operation")
		}
		if err != nil {
			return err
		}
		data, err := jsonBytes(status)
		if err != nil {
			return err
		}
		_, err = out.Write(data)
		return err
	}
	if command == "open" {
		if len(positions) != 0 || get("directory") == "" {
			return fmt.Errorf("open requires --directory <prepared-source-directory>")
		}
		if err := resumeDesktop(ctx, get("directory")); err != nil {
			return err
		}
		fmt.Fprintln(out, "Copilot App accepted the source directory. Start a session in an app-created worktree, then use the Radius handoff review. No graph readiness is implied.")
		return nil
	}
	if (command == "verify" && len(positions) > 1) || (command != "verify" && len(positions) != 1) {
		return fmt.Errorf("%s requires one target (optional for verify)", command)
	}
	target := ""
	if len(positions) > 0 {
		target = positions[0]
	}
	switch command {
	case "inspect":
		p, err := LoadPack(target)
		if err != nil {
			return err
		}
		b, err := jsonBytes(struct {
			ManifestSHA256   string   `json:"manifestSha256"`
			Manifest         Manifest `json:"manifest"`
			DeploymentStatus string   `json:"deploymentStatus"`
		}{p.Digest, p.Manifest, "unverified by Radpack"})
		if err != nil {
			return err
		}
		_, err = out.Write(b)
		return err
	case "install":
		result, err := InstallApplication(ctx, target, ApplicationOptions{Directory: get("directory"), Pack: get("pack"), PackPath: get("pack-path"),
			Latest: get("latest") != "", Adapt: get("adapt") != "", VersionProbe: probe, Desktop: get("no-open") == ""}, func(s string) { fmt.Fprintln(out, s) })
		if err != nil {
			return err
		}
		if result.Status == "handoff-pending" {
			fmt.Fprintf(out, "Source prepared at %s. The Radius overlay is staged privately and has not been applied to this clone.\n", result.Directory)
			fmt.Fprintln(out, "Start the app session from the recorded source revision. A compatible Radius extension will offer to apply the pack to its clean worktree and show the graph after approval.")
			fmt.Fprintln(out, "If Radius is missing, install radius from awesome-copilot through the app's Customize page. Keep an existing local development build; do not install a duplicate. Sign-in, trust, and plugin consent remain in the app.")
			if err := launchCopilot(ctx, result.Directory); err != nil {
				return err
			}
			fmt.Fprintln(out, "Copilot App accepted the directory. Session start and Radius approval are still required; the graph is not yet ready.")
			return nil
		}
		if result.Status == "adaptation-required" {
			fmt.Fprintf(out, "Prepared latest source in %s.\nStatus: adaptation-required (not model-validated or deployment-verified).\nOpen this checkout in Copilot with the Radius tools and follow .radpack/ADAPT.md.\nConflicts and reference assets are preserved; no agent or Radius tool was executed.\n", result.Directory)
		} else {
			fmt.Fprintf(out, "Installed and verified files in %s.\nOpen this checkout in Radius Canvas, then create your own Radius environment before deploying.\n", result.Directory)
		}
		fmt.Fprintln(out, "No environments, credentials, workflows, or cloud resources were created.")
	case "verify":
		if get("directory") == "" {
			return fmt.Errorf("verify requires --directory")
		}
		result, err := VerifyInstallation(ctx, target, get("directory"))
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Verified %d declared files for %s at upstream %s.\nStatus: %s.\nThis verifies installation content, not adaptation, compilation, or deployment.\n", result.Files, result.Name, result.Upstream, result.Status)
	}
	return nil
}
