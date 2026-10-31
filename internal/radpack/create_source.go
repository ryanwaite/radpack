package radpack

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type preparedSource struct {
	Options CreateOptions
	Note    string
	temp    string
}

func (s *preparedSource) close() error {
	if err := os.RemoveAll(s.temp); err != nil {
		return fmt.Errorf("source verification cleanup failed: %w", err)
	}
	return nil
}

func sourceRepositories(ctx context.Context, source string) ([]string, error) {
	remotes, err := gitText(ctx, source, "remote")
	if err != nil {
		return nil, err
	}
	repositories := map[string]string{}
	for _, remote := range strings.Fields(remotes) {
		raw, err := gitText(ctx, source, "config", "--get", "remote."+remote+".url")
		if err != nil {
			return nil, err
		}
		repository, err := repositoryURL(raw)
		if err != nil {
			return nil, fmt.Errorf("a remote is not a supported credential-free GitHub source; specify --upstream owner/repo")
		}
		repositories[strings.ToLower(repository)] = repository
	}
	result := []string{}
	for _, key := range sortedKeys(repositories) {
		result = append(result, repositories[key])
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no source remote found; specify --upstream owner/repo")
	}
	return result, nil
}

func remoteDefaultBranch(ctx context.Context, repository string) (string, string, error) {
	output, err := gitText(ctx, "", "ls-remote", "--symref", repository, "HEAD")
	if err != nil {
		return "", "", err
	}
	var branch, commit string
	for _, line := range strings.Split(output, "\n") {
		value, name, ok := strings.Cut(line, "\t")
		if !ok || name != "HEAD" {
			continue
		}
		if strings.HasPrefix(value, "ref: refs/heads/") {
			branch = strings.TrimPrefix(value, "ref: refs/heads/")
		} else if commitPattern.MatchString(value) {
			commit = value
		}
	}
	if branch == "" || commit == "" {
		return "", "", fmt.Errorf("remote default branch could not be determined; specify --base with a full published commit SHA")
	}
	return branch, commit, nil
}

// Verify fetchability in a private scratch Git database, without changing the
// author's refs, index, working files, or remotes. Its index is later used to
// compare a remote baseline with the author's working tree.
func prepareCreateSource(ctx context.Context, o CreateOptions, ui *createUI) (result *preparedSource, err error) {
	if o.Source == "" {
		o.Source = "."
	}
	o.Source, err = repositoryRoot(ctx, o.Source)
	if err != nil {
		return nil, err
	}
	if o.Upstream == "" {
		repositories, err := sourceRepositories(ctx, o.Source)
		if err != nil {
			return nil, err
		}
		if len(repositories) == 1 {
			o.Upstream = repositories[0]
		} else {
			if ui == nil {
				return nil, fmt.Errorf("multiple source repositories are configured; specify --upstream to choose the fork or original repository")
			}
			fmt.Fprintln(ui.out, "Choose the repository consumers should clone (fork and upstream may differ):")
			for i, repository := range repositories {
				fmt.Fprintf(ui.out, "  %d. %s\n", i+1, repository)
			}
			answer, err := ui.line(ctx, "Source repository number (Enter cancels): ")
			if err != nil {
				return nil, err
			}
			if answer == "" {
				return nil, errCreateCancelled
			}
			index, err := strconv.Atoi(answer)
			if err != nil || index < 1 || index > len(repositories) {
				return nil, fmt.Errorf("invalid source selection; no files written")
			}
			o.Upstream = repositories[index-1]
		}
	}
	o.Upstream, err = repositoryURL(o.Upstream)
	if err != nil {
		return nil, err
	}
	if o.Base != "" && !commitPattern.MatchString(o.Base) {
		return nil, fmt.Errorf("--base must be a full 40-character published commit SHA")
	}
	temp, err := os.MkdirTemp("", "radpack-source-")
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			if cleanupErr := os.RemoveAll(temp); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("source verification cleanup failed: %w", cleanupErr))
			}
		}
	}()
	database := filepath.Join(temp, "baseline")
	if err := os.Mkdir(database, 0700); err != nil {
		return nil, err
	}
	if _, err := git(ctx, database, "init", "--quiet", "--template="); err != nil {
		return nil, err
	}
	if _, err := git(ctx, database, "remote", "add", "origin", o.Upstream); err != nil {
		return nil, err
	}
	fetch := func(commit string) error {
		if _, err := git(ctx, database, "fetch", "--quiet", "--no-tags", "--depth=1", "origin", commit); err != nil {
			return err
		}
		actual, err := gitText(ctx, database, "rev-parse", "FETCH_HEAD^{commit}")
		if err != nil {
			return err
		}
		if actual != commit {
			return fmt.Errorf("published revision differs from the requested commit")
		}
		return nil
	}
	note := "Explicit source revision, verified fetchable from the selected repository."
	if o.Base == "" {
		head, err := gitText(ctx, o.Source, "rev-parse", "HEAD")
		if err != nil {
			return nil, err
		}
		if err := fetch(head); err == nil {
			o.Base = head
			note = "Current checkout commit, verified published. Existing source assets will be retained."
		} else {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			branch, remoteHead, lookupErr := remoteDefaultBranch(ctx, o.Upstream)
			if lookupErr != nil {
				return nil, fmt.Errorf("checkout HEAD could not be verified as published; check repository access or specify --upstream and --base: %w", lookupErr)
			}
			if ui == nil {
				return nil, fmt.Errorf("checkout HEAD is not confirmed published. Remote default branch %q is at %s. Review the intended source and pass --base explicitly, or run interactively", branch, remoteHead)
			}
			fmt.Fprintf(ui.out, "Checkout HEAD %s could not be fetched from %s.\nThe published default branch %q is at %s.\nLocal-only commits and working edits will be reviewed against that revision; application changes stay excluded unless selected.\n", head, o.Upstream, branch, remoteHead)
			yes, err := ui.confirm(ctx, "Use that published source revision?")
			if err != nil {
				return nil, err
			}
			if !yes {
				return nil, errCreateCancelled
			}
			o.Base = remoteHead
			note = "Published default-branch revision, explicitly chosen for this review."
			if err := fetch(o.Base); err != nil {
				return nil, err
			}
		}
	} else if err := fetch(o.Base); err != nil {
		return nil, fmt.Errorf("source revision is not confirmed available from the intended repository; no files written: %w", err)
	}
	if _, err := git(ctx, database, "read-tree", o.Base); err != nil {
		return nil, err
	}
	o.BaselineRoot = database
	keep = true
	return &preparedSource{Options: o, Note: note, temp: temp}, nil
}
