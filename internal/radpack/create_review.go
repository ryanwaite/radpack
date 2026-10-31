package radpack

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/term"
)

var errCreateCancelled = errors.New("create cancelled; no pack written")

type createUI struct {
	reader *bufio.Reader
	out    io.Writer
}

func terminalUI(input *os.File, output io.Writer) *createUI {
	file, ok := output.(*os.File)
	if !ok || !term.IsTerminal(int(input.Fd())) || !term.IsTerminal(int(file.Fd())) {
		return nil
	}
	return &createUI{bufio.NewReader(input), output}
}

func (ui *createUI) line(ctx context.Context, question string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := fmt.Fprint(ui.out, question); err != nil {
		return "", err
	}
	type answer struct {
		text string
		err  error
	}
	answers := make(chan answer, 1)
	// A cancelled terminal read can remain blocked until process exit. Do not
	// start another read after cancellation; the buffered channel lets it finish.
	go func() {
		bytes, err := ui.reader.ReadSlice('\n')
		answers <- answer{strings.TrimSpace(string(bytes)), err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-answers:
		if result.err == io.EOF {
			return "", errCreateCancelled
		}
		if result.err != nil {
			return "", fmt.Errorf("could not read review response; no pack written")
		}
		return result.text, nil
	}
}

func (ui *createUI) confirm(ctx context.Context, question string) (bool, error) {
	answer, err := ui.line(ctx, question+" [y/N] ")
	if err != nil {
		return false, err
	}
	switch strings.ToLower(answer) {
	case "y", "yes":
		return true, nil
	case "", "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("answer yes or no; no pack written")
	}
}

func printCreateReview(out io.Writer, p *Plan, output, sourceNote string) {
	version := "unknown"
	if p.Manifest.RadiusVersion != nil {
		version = *p.Manifest.RadiusVersion
	}
	fmt.Fprintf(out, "Pack: %s\nConsumers clone: %s\nSource revision: %s\n%s\nOutput: %s\nRadius release annotation: %s\n",
		p.Manifest.Name, p.Manifest.Upstream.Repository, p.Manifest.Upstream.Commit, sourceNote, output, version)
	fmt.Fprintf(out, "Radius version source: %s\nModel validation record: %s\n", p.RadiusEvidence.Source, p.RadiusEvidence.Validation)
	for _, warning := range append(append([]string{}, p.RadiusEvidence.Warnings...), p.BuildWarnings...) {
		fmt.Fprintf(out, "Warning: %s\n", warning)
	}
	fmt.Fprintln(out, "\nWhat consumers receive:")
	labels := map[string]string{"retain": "retain upstream", "add": "add", "replace": "replace", "delete": "delete"}
	for _, file := range p.Manifest.Files {
		fmt.Fprintf(out, "  %-16s %s\n", labels[file.Operation], file.Path)
	}
	if len(p.Excluded) > 0 {
		fmt.Fprintln(out, "\nExcluded (not portable pack inputs):")
		for _, file := range p.Excluded {
			fmt.Fprintf(out, "  %q: %s\n", file.Path, file.Reason)
		}
	}
	if len(p.Unselected) > 0 {
		fmt.Fprintln(out, "\nApplication changes not selected (excluded from the pack by default):")
		for i, file := range p.Unselected {
			fmt.Fprintf(out, "  %d. %q\n", i+1, file)
		}
	}
	fmt.Fprintln(out, "\nDeployment verification: not established by Radpack. A graph or origin record is not a deployment test.")
	fmt.Fprintln(out, "Review selected file contents before saving. Secret detection is best-effort; credentials and environment state do not belong in a pack.")
}

func selection(answer string, candidates []string) ([]string, error) {
	if answer == "" {
		return nil, nil
	}
	result := []string{}
	seen := map[int]bool{}
	for _, value := range strings.Split(answer, ",") {
		index, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || index < 1 || index > len(candidates) || seen[index] {
			return nil, fmt.Errorf("select distinct file numbers separated by commas; no pack written")
		}
		seen[index] = true
		result = append(result, candidates[index-1])
	}
	return result, nil
}

func createCommand(ctx context.Context, values map[string][]string, out io.Writer, ui *createUI) (err error) {
	get := func(key string) string {
		if len(values[key]) > 0 {
			return values[key][0]
		}
		return ""
	}
	if get("approve") != "" && get("dry-run") != "" {
		return fmt.Errorf("use --dry-run or --approve, not both")
	}
	// Automation flags never enable prompts, even when run from a terminal.
	if get("dry-run") != "" || get("approve") != "" {
		ui = nil
	}
	var version *string
	if _, ok := values["radius-version"]; ok {
		v := get("radius-version")
		version = &v
	}
	source, err := prepareCreateSource(ctx, CreateOptions{
		Source: get("source"), Base: get("base"), Upstream: get("upstream"),
		Name: get("name"), RadiusVersion: version, Include: values["include"],
	}, ui)
	if err != nil {
		if errors.Is(err, errCreateCancelled) {
			fmt.Fprintln(out, "Cancelled. No pack written.")
			return nil
		}
		return err
	}
	defer func() { err = errors.Join(err, source.close()) }()
	p, err := PlanCreate(ctx, source.Options)
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
	printCreateReview(out, p, output, source.Note)
	if len(p.Blockers) > 0 {
		return fmt.Errorf("export blocked:\n  %s", strings.Join(p.Blockers, "\n  "))
	}
	if ui != nil && len(p.Unselected) > 0 {
		answer, err := ui.line(ctx, "Include individual application changes? Enter file numbers separated by commas, or Enter for none: ")
		if errors.Is(err, errCreateCancelled) {
			fmt.Fprintln(out, "Cancelled. No pack written.")
			return nil
		}
		if err != nil {
			return err
		}
		included, err := selection(answer, p.Unselected)
		if err != nil {
			return err
		}
		if len(included) > 0 {
			source.Options.Include = append(source.Options.Include, included...)
			p, err = PlanCreate(ctx, source.Options)
			if err != nil {
				return err
			}
			fmt.Fprintln(out, "\nUpdated review:")
			printCreateReview(out, p, output, source.Note)
			if len(p.Blockers) > 0 {
				return fmt.Errorf("export blocked:\n  %s", strings.Join(p.Blockers, "\n  "))
			}
		}
	}
	approval := get("approve")
	if ui != nil {
		yes, err := ui.confirm(ctx, "Create this pack?")
		if errors.Is(err, errCreateCancelled) || (err == nil && !yes) {
			fmt.Fprintln(out, "Cancelled. No pack written.")
			return nil
		}
		if err != nil {
			return err
		}
		approval = p.Approval
	}
	if approval == "" {
		fmt.Fprintf(out, "\nReview SHA: %s\nNo files written. Use --include <file> for additional application changes.\nRepeat with the same options and --approve <review-sha> to save, or run in an interactive terminal for confirmation.\n", p.Approval)
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Re-read every selected file, evidence record and inventory after the human
	// response. Never save a snapshot that changed while the question was open.
	fresh, err := PlanCreate(ctx, source.Options)
	if err != nil {
		return err
	}
	if len(fresh.Blockers) > 0 || fresh.Approval != p.Approval {
		return fmt.Errorf("checkout changed during review; no pack written. Run create again")
	}
	if _, err := WritePack(fresh, output, approval); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nCreated %s. Deployment verification remains unverified by Radpack.\n", output)
	return nil
}
