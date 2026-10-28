package radpack

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

type limitedBuffer struct{ buffer bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > maxPackBytes {
		return 0, fmt.Errorf("Git output exceeds limit")
	}
	return b.buffer.Write(p)
}

func git(ctx context.Context, cwd string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{
		"-c", "core.hooksPath=" + os.DevNull, "-c", "protocol.ext.allow=never",
		"-c", "core.fsmonitor=false", "-c", "core.autocrlf=false",
	}, args...)...)
	command.Dir = cwd
	skip := map[string]bool{"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_COMMON_DIR": true,
		"GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
		"GIT_TERMINAL_PROMPT": true, "GIT_LFS_SKIP_SMUDGE": true, "GIT_PAGER": true}
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		if !skip[key] {
			command.Env = append(command.Env, env)
		}
	}
	command.Env = append(command.Env, "GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1", "GIT_PAGER=cat")
	var output limitedBuffer
	command.Stdout = &output
	// Never expose raw Git stderr: transport helpers can include credentials.
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("Git %s failed; check repository access and revision (raw output withheld)", args[0])
	}
	return output.buffer.Bytes(), nil
}
func gitText(ctx context.Context, cwd string, args ...string) (string, error) {
	b, err := git(ctx, cwd, args...)
	return strings.TrimSpace(string(b)), err
}

var repoPattern = regexp.MustCompile(`^(https://github\.com/|git@github\.com:)?([A-Za-z0-9-]+)/([A-Za-z0-9_.-]+?)(\.git)?$`)

func repositoryURL(value string) (string, error) {
	m := repoPattern.FindStringSubmatch(value)
	if m == nil || m[3] == "." || m[3] == ".." {
		return "", fmt.Errorf("upstream must be owner/repo or a credential-free github.com repository URL")
	}
	return "https://github.com/" + m[2] + "/" + m[3] + ".git", nil
}

type treeEntry struct{ Mode, Type, Object string }

func treeAt(ctx context.Context, source, commit string) (map[string]treeEntry, error) {
	output, err := git(ctx, source, "ls-tree", "-r", "-z", commit)
	if err != nil {
		return nil, err
	}
	result := map[string]treeEntry{}
	for _, record := range splitNull(output) {
		meta, file, ok := strings.Cut(record, "\t")
		parts := strings.Fields(meta)
		if !ok || len(parts) != 3 {
			return nil, fmt.Errorf("cannot read upstream tree")
		}
		result[file] = treeEntry{parts[0], parts[1], parts[2]}
	}
	return result, nil
}
func cloneRevision(ctx context.Context, destination, repository, commit string) error {
	for _, args := range [][]string{{"init", "--quiet", "--template="}, {"remote", "add", "origin", repository},
		{"fetch", "--quiet", "--no-tags", "--depth=1", "origin", commit}} {
		if _, err := git(ctx, destination, args...); err != nil {
			return err
		}
	}
	actual, err := gitText(ctx, destination, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return err
	}
	if actual != commit {
		return fmt.Errorf("fetched revision differs from requested commit")
	}
	_, err = git(ctx, destination, "checkout", "--quiet", "-b", "radpack", commit)
	return err
}
func resolveRevision(ctx context.Context, repository, ref string) (string, error) {
	if _, err := repositoryURL(repository); err != nil {
		return "", err
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`).MatchString(ref) || strings.Contains(ref, "..") || strings.HasSuffix(ref, "/") {
		return "", fmt.Errorf("invalid Git revision selector")
	}
	if commitPattern.MatchString(ref) {
		return ref, nil
	}
	selectors := []string{"HEAD"}
	if ref != "HEAD" {
		selectors = []string{"refs/heads/" + ref, "refs/tags/" + ref, "refs/tags/" + ref + "^{}"}
	}
	output, err := gitText(ctx, "", append([]string{"ls-remote", repository}, selectors...)...)
	if err != nil {
		return "", err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		sha, name, ok := strings.Cut(line, "\t")
		if ok {
			refs[name] = sha
		}
	}
	if refs["refs/heads/"+ref] != "" && refs["refs/tags/"+ref] != "" {
		return "", fmt.Errorf("revision names both a branch and a tag; use an exact SHA")
	}
	for _, name := range []string{"refs/tags/" + ref + "^{}", "refs/tags/" + ref, "refs/heads/" + ref, "HEAD"} {
		if sha := refs[name]; commitPattern.MatchString(sha) {
			return sha, nil
		}
	}
	return "", fmt.Errorf("remote revision was not found")
}
func splitNull(b []byte) []string {
	result := []string{}
	for _, s := range strings.Split(string(b), "\x00") {
		if s != "" {
			result = append(result, s)
		}
	}
	return result
}
