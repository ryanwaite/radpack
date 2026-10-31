package radpack

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

func validateUpstreamTree(tree map[string]treeEntry) error {
	for _, file := range sortedKeys(tree) {
		lower := strings.ToLower(file)
		if (lower == ".radpack" || strings.HasPrefix(lower, ".radpack/")) && file != ".radpack/radpack.json" {
			return fmt.Errorf("upstream uses reserved Radpack installation metadata paths")
		}
		if strings.HasPrefix(lower, ".radius/") || strings.HasPrefix(lower, ".rad/") || strings.HasPrefix(lower, ".kube/") ||
			lower == ".radius-credentials.json" || lower == ".radius-target-kubeconfig" ||
			path.Base(lower) == "radius-environment.bicep" || path.Base(lower) == "radius-recipe-pack.bicep" ||
			lower == "app-graph.json" {
			reason, err := excludedReason(file)
			if err != nil {
				return err
			}
			if reason != "" {
				return fmt.Errorf("upstream contains non-portable Radius environment material: %q; choose a source revision without these files", file)
			}
		}
	}
	return nil
}

// These are review warnings, not a Bicep parser or a claim that a build works.
// Only Git source literals are inspected; no pinned source is rewritten.
func buildSourceWarnings(manifest Manifest, assets map[string][]byte) []string {
	warnings := []string{}
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			warnings = append(warnings, s)
		}
	}
	pattern := regexp.MustCompile(`git::[^'"\s]+`)
	for _, file := range sortedKeys(assets) {
		if !strings.HasSuffix(file, ".bicep") {
			continue
		}
		for _, raw := range pattern.FindAllString(string(assets[file]), -1) {
			if strings.Contains(raw, "${") {
				add(fmt.Sprintf("%s contains a dynamic Git build source; its resolved repository and revision must be checked before deployment.", file))
				continue
			}
			u, err := url.Parse(strings.TrimPrefix(raw, "git::"))
			if err != nil || u.Host != "github.com" || u.Scheme != "https" || u.User != nil {
				add(fmt.Sprintf("%s contains a Git build source that cannot be compared with the selected application revision.", file))
				continue
			}
			parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "//", 2)
			repository, err := repositoryURL(parts[0])
			if err != nil {
				add(fmt.Sprintf("%s contains an unrecognized Git repository reference; inspect its build inputs.", file))
				continue
			}
			if !strings.EqualFold(repository, manifest.Upstream.Repository) {
				add(fmt.Sprintf("%s builds from another repository (%s); check whether this is an intentional dependency or application code that must match the selected checkout.", file, repository))
				continue
			}
			refs := u.Query()["ref"]
			if len(refs) != 1 || !commitPattern.MatchString(refs[0]) {
				add(fmt.Sprintf("%s has an application Git build source without a full immutable commit SHA; its build may not reproduce the selected source.", file))
			} else if refs[0] != manifest.Upstream.Commit {
				add(fmt.Sprintf("%s builds application code at %s, but consumers clone %s. Verify that the build contains the intended source and adaptations.", file, refs[0], manifest.Upstream.Commit))
			}
			context := ""
			if len(parts) == 2 {
				context = path.Clean(parts[1])
				if err := safePath(context); err != nil {
					add(fmt.Sprintf("%s has an invalid or unsupported Git build context; inspect its build inputs.", file))
					continue
				}
				context += "/"
			}
			for _, operation := range manifest.Files {
				if operation.Operation == "retain" || !strings.HasPrefix(operation.Path, context) {
					continue
				}
				add(fmt.Sprintf("%s builds from Git, while the pack changes %s inside that build context. Local overlays are not sent to a remote builder; publish the assembled source or use a supported local build path before deployment.", file, operation.Path))
			}
		}
	}
	return warnings
}
