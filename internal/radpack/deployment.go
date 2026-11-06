package radpack

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const deploymentBindingFile = "radpack-deployment.json"

// Deployment identity is local to the clone, shared by its worktrees, and never
// part of a portable pack. Status is persisted before any remote mutation.
type deploymentBinding struct {
	FormatVersion     int               `json:"formatVersion"`
	CommonDirectory   string            `json:"commonDirectory"`
	Application       Upstream          `json:"application"`
	Repository        string            `json:"repository"`
	Account           string            `json:"account"`
	AccountID         int64             `json:"accountId"`
	RepositoryID      int64             `json:"repositoryId"`
	Status            string            `json:"status"`
	ReviewSHA256      string            `json:"reviewSha256"`
	OriginalOrigin    string            `json:"originalOrigin"`
	OriginalUpstream  string            `json:"originalUpstream"`
	CreationAttempted bool              `json:"creationAttempted"`
	Kind              string            `json:"kind,omitempty"`
	Private           bool              `json:"private,omitempty"`
	SnapshotCommit    string            `json:"snapshotCommit,omitempty"`
	SnapshotTree      string            `json:"snapshotTree,omitempty"`
	PushAttempted     bool              `json:"pushAttempted,omitempty"`
	PublicationReview *RepositoryReview `json:"publicationReview,omitempty"`
}

func readDeploymentBinding(ctx context.Context, root string) (*deploymentBinding, error) {
	common, err := commonDirectory(ctx, root)
	if err != nil {
		return nil, err
	}
	file, err := readRegular(common, deploymentBindingFile)
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, nil
	}
	var binding deploymentBinding
	if err := decodeStrict(file.Bytes, &binding); err != nil {
		return nil, err
	}
	upstream, err := repositoryURL(binding.Application.Repository)
	if err != nil || upstream != binding.Application.Repository ||
		binding.FormatVersion != 1 || binding.CommonDirectory != common ||
		!commitPattern.MatchString(binding.Application.Commit) || !hashPattern.MatchString(binding.ReviewSHA256) ||
		binding.AccountID <= 0 || !match(`^[A-Za-z0-9][A-Za-z0-9-]*$`, binding.Account) ||
		(binding.Status != "pending" && binding.Status != "ready") {
		return nil, fmt.Errorf("invalid local deployment repository binding")
	}
	destination, err := repositoryURL(binding.Repository)
	if err != nil || destination != binding.Repository ||
		!strings.EqualFold(strings.Split(strings.TrimPrefix(destination, "https://github.com/"), "/")[0], binding.Account) ||
		strings.EqualFold(destination, upstream) || (binding.Status == "ready" && binding.RepositoryID <= 0) {
		return nil, fmt.Errorf("invalid personal deployment repository identity")
	}
	if binding.Kind != "" && binding.Kind != "independent" {
		return nil, fmt.Errorf("unknown deployment repository kind")
	}
	if binding.Kind == "independent" {
		r := binding.PublicationReview
		if r == nil || !commitPattern.MatchString(binding.SnapshotTree) ||
			(binding.SnapshotCommit != "" && !commitPattern.MatchString(binding.SnapshotCommit)) ||
			(binding.Status == "ready" && binding.SnapshotCommit == "") ||
			r.Repository != binding.Repository || r.Application != binding.Application ||
			r.Tree != binding.SnapshotTree || r.Private != binding.Private ||
			r.Account.ID != binding.AccountID || r.Account.Login != binding.Account || r.CommonDirectory != common ||
			r.Branch != authoringBranch || !commitPattern.MatchString(r.LocalHead) || r.LocalBranch == "" {
			return nil, fmt.Errorf("invalid independent repository publication journal")
		}
		digest, err := repositoryReviewHash(*r)
		if err != nil || digest != binding.ReviewSHA256 {
			return nil, fmt.Errorf("independent repository approval journal changed")
		}
	}
	original, err := repositoryURL(binding.OriginalOrigin)
	if err != nil || original != upstream {
		return nil, fmt.Errorf("invalid original source remote in deployment binding")
	}
	if binding.OriginalUpstream != "" {
		original, err = repositoryURL(binding.OriginalUpstream)
		if err != nil || original != upstream {
			return nil, fmt.Errorf("invalid preserved upstream remote")
		}
	}
	return &binding, nil
}

// Write a complete local journal atomically. Never follow a state symlink or
// leave a half-written JSON file when an operation is interrupted.
func writeDeploymentBinding(root string, binding deploymentBinding) error {
	if _, err := readRegular(root, deploymentBindingFile); err != nil {
		return err
	}
	data, err := jsonBytes(binding)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(root, ".radpack-deployment-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(root, deploymentBindingFile))
}

type deploymentRemotes struct {
	Origin   string `json:"origin"`
	Upstream string `json:"upstream"`
}

func readDeploymentRemotes(ctx context.Context, root string) (deploymentRemotes, error) {
	// List only remote URLs and routing fields. Never expose raw config output.
	data, err := gitText(ctx, root, "config", "--get-regexp", `^remote\.(origin|upstream)\.(url|pushurl|mirror)$`)
	if err != nil {
		return deploymentRemotes{}, err
	}
	values := map[string][]string{}
	for _, line := range strings.Split(data, "\n") {
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			return deploymentRemotes{}, fmt.Errorf("unrecognized remote configuration")
		}
		values[key] = append(values[key], value)
	}
	if len(values["remote.origin.url"]) != 1 || len(values["remote.upstream.url"]) > 1 {
		return deploymentRemotes{}, fmt.Errorf("fork setup requires exactly one origin URL and at most one upstream URL")
	}
	for _, name := range []string{"origin", "upstream"} {
		if len(values["remote."+name+".pushurl"]) > 0 || len(values["remote."+name+".mirror"]) > 0 {
			return deploymentRemotes{}, fmt.Errorf("fork setup will not change remotes with explicit push URLs or mirror settings; review Git configuration first")
		}
	}
	remotes := deploymentRemotes{Origin: values["remote.origin.url"][0]}
	if len(values["remote.upstream.url"]) == 1 {
		remotes.Upstream = values["remote.upstream.url"][0]
	}
	for _, raw := range []string{remotes.Origin, remotes.Upstream} {
		if raw != "" {
			if _, err := repositoryURL(raw); err != nil {
				return deploymentRemotes{}, err
			}
		}
	}
	return remotes, nil
}

func validateDeploymentRemotes(ctx context.Context, root string, binding *deploymentBinding) error {
	remotes, err := readDeploymentRemotes(ctx, root)
	if err != nil {
		return err
	}
	origin, err := repositoryURL(remotes.Origin)
	if err != nil {
		return err
	}
	upstream, err := repositoryURL(remotes.Upstream)
	if err != nil {
		return fmt.Errorf("deployment repository requires the original application repository as upstream")
	}
	if binding.Status != "ready" {
		return fmt.Errorf("deployment repository setup is incomplete; resume the recorded setup before continuing")
	}
	if origin != binding.Repository || upstream != binding.Application.Repository {
		return fmt.Errorf("Git remotes differ from the approved deployment repository binding")
	}
	return nil
}

// Keep verification strict while permitting an explicitly recorded personal fork.
// The receipt and pack continue to identify the original application.
func verifyApplicationOrigin(ctx context.Context, root, application string) error {
	binding, err := readDeploymentBinding(ctx, root)
	if err != nil {
		return err
	}
	if binding != nil {
		if binding.Application.Repository != application {
			return fmt.Errorf("deployment binding belongs to a different application source")
		}
		return validateDeploymentRemotes(ctx, root, binding)
	}
	origin, err := gitText(ctx, root, "config", "--get", "remote.origin.url")
	if err != nil {
		return err
	}
	origin, err = repositoryURL(origin)
	if err != nil {
		return err
	}
	if origin != application {
		return fmt.Errorf("installed origin differs from pack upstream; use explicit fork setup rather than silently changing repository identity")
	}
	return nil
}

func boundApplication(ctx context.Context, root string) (*Upstream, error) {
	binding, err := readDeploymentBinding(ctx, root)
	if err != nil || binding == nil {
		return nil, err
	}
	if err := validateDeploymentRemotes(ctx, root, binding); err != nil {
		return nil, err
	}
	application := binding.Application
	return &application, nil
}
