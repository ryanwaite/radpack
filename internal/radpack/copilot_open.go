package radpack

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func runCopilotOpen(ctx context.Context, executable, directory string, newSession bool) error {
	root, err := canonicalDirectory(directory)
	if err != nil {
		return err
	}
	if executable == "" {
		executable, err = exec.LookPath("github")
		if err != nil && runtime.GOOS == "darwin" {
			candidate := "/Applications/GitHub.app/Contents/MacOS/github"
			if info, e := os.Stat(candidate); e == nil && info.Mode().IsRegular() {
				executable = candidate
				err = nil
			}
		}
		if err != nil {
			return fmt.Errorf("source checkout and pending handoff are saved at %s, but the Copilot App command was not found; install the app and retry with radpack open --directory <source-directory>", root)
		}
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return fmt.Errorf("Copilot App executable is unavailable; the prepared checkout is unchanged")
	}
	if rel, e := filepath.Rel(root, executable); e == nil && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to run a Copilot App executable inside downloaded application source")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := []string{"open"}
	if newSession {
		args = append(args, "--new")
	}
	args = append(args, root)
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.WaitDelay = time.Second
	// Do not expose incidental output from another application's startup.
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Copilot App did not acknowledge opening the prepared source checkout; files and handoff are preserved. Retry with radpack open --directory <source-directory>")
	}
	return nil
}
