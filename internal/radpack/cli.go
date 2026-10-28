package radpack

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

const help = `Radpack - set up cloud-native apps, explore their architecture, and deploy to your cloud of choice with Radius

  radpack create --base <full-sha> [--source <repo>] [--output <pack-directory>]
                 [--upstream <owner/repo>] [--name <name>] [--include <file>]...
                 [--radius-version <version>] [--dry-run | --approve <review-sha>]
  radpack inspect <pack-directory>
  radpack install <owner/repo|github-url|pack-directory> [--directory <new-directory>]
                  [--pack <local-directory|github-tree-url> | --pack-path <repo-directory>]
                  [--latest --adapt]
  radpack verify [<pack-directory>] --directory <installed-directory>

Create previews the inventory without writing until --approve matches its review.
Output defaults to ./contrib/<pack-name>. Existing destinations are never reused.
Install never creates Radius environments, executes pack scripts, or deploys.
Install defaults to the pack's reference commit. --latest --adapt prepares a
Radius authoring handoff, NOT an automatically adapted or deployment-ready app.
Verify checks file reproduction, not Radius compilation or cloud functionality.
`

// Keep interspersed flags and repeated --include compatible with the original
// CLI without adding a runtime or external command framework dependency.
func parseOptions(command string, args []string) (map[string][]string, []string, error) {
	schemas := map[string]map[string]bool{
		"create":  {"source": false, "base": false, "output": false, "upstream": false, "name": false, "include": false, "radius-version": false, "dry-run": true, "approve": false},
		"inspect": {},
		"install": {"directory": false, "pack": false, "pack-path": false, "latest": true, "adapt": true},
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
		if key != "include" && len(values[key]) > 0 {
			return nil, nil, fmt.Errorf("duplicate option --%s", key)
		}
		values[key] = append(values[key], value)
	}
	return values, positions, nil
}

func Run(ctx context.Context, args []string, out, errOut io.Writer) int {
	err := run(ctx, args, out)
	if err != nil {
		fmt.Fprintf(errOut, "radpack: %s\n", err)
		return 1
	}
	return 0
}
func run(ctx context.Context, args []string, out io.Writer) error {
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
	if command == "create" {
		if len(positions) > 0 {
			return fmt.Errorf("create takes options, not positional arguments")
		}
		if get("approve") != "" && get("dry-run") != "" {
			return fmt.Errorf("use --dry-run or --approve, not both")
		}
		var version *string
		if _, ok := values["radius-version"]; ok {
			s := get("radius-version")
			version = &s
		}
		p, err := PlanCreate(ctx, CreateOptions{get("source"), get("base"), get("upstream"), get("name"), version, values["include"]})
		if err != nil {
			return err
		}
		output := get("output")
		if output == "" {
			output = filepath.Join("contrib", p.Manifest.Name)
		}
		output, err = filepath.Abs(output)
		if err != nil {
			return err
		}
		versionLabel := "unspecified; inspect the included Bicep configuration"
		if p.Manifest.RadiusVersion != nil {
			versionLabel = *p.Manifest.RadiusVersion
		}
		fmt.Fprintf(out, "Pack: %s\nUpstream: %s@%s\nOutput: %s\nRadius release annotation: %s\n\nSelected files:\n", p.Manifest.Name, p.Manifest.Upstream.Repository, p.Manifest.Upstream.Commit, output, versionLabel)
		for _, file := range p.Manifest.Files {
			fmt.Fprintf(out, "  %-7s %s\n", file.Operation, file.Path)
		}
		if len(p.Excluded) > 0 {
			fmt.Fprintln(out, "\nExcluded:")
			for _, file := range p.Excluded {
				fmt.Fprintf(out, "  %q: %s\n", file.Path, file.Reason)
			}
		}
		if len(p.Unselected) > 0 {
			fmt.Fprintln(out, "\nNot selected (use --include <file> for a required application change):")
			for _, file := range p.Unselected {
				fmt.Fprintf(out, "  %q\n", file)
			}
		}
		if len(p.Blockers) > 0 {
			return fmt.Errorf("export blocked:\n  %s", strings.Join(p.Blockers, "\n  "))
		}
		if get("approve") != "" {
			if _, err := WritePack(p, output, get("approve")); err != nil {
				return err
			}
			fmt.Fprintf(out, "\nCreated %s. Cloud deployment status: unverified by Radpack.\n", output)
		} else {
			fmt.Fprintf(out, "\nReview SHA: %s\nNo files written. Review the selected file contents, then repeat without --dry-run and add --approve <review-sha>.\nSecret detection is best-effort, not a guarantee. No deployment verification is implied.\n", p.Approval)
		}
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
			Latest: get("latest") != "", Adapt: get("adapt") != ""}, func(s string) { fmt.Fprintln(out, s) })
		if err != nil {
			return err
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
