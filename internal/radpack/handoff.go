package radpack

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"
)

// Handoffs belong to the local user, never to a marker in downloaded source.
// The canonical Git common directory binds a request to one clone and its
// worktrees; matching repository names alone must never authorize application.
type handoffRecord struct {
	ProtocolVersion    int    `json:"protocolVersion"`
	RequestID          string `json:"requestId"`
	SourceDirectory    string `json:"sourceDirectory"`
	CommonDirectory    string `json:"commonDirectory"`
	ApplicationCommit  string `json:"applicationCommit"`
	PackManifestSHA256 string `json:"packManifestSha256"`
	InstallationStatus string `json:"installationStatus"`
	CreatedAt          string `json:"createdAt"`
	ExpiresAt          string `json:"expiresAt"`
}

type handoffClaim struct {
	RequestID         string `json:"requestId"`
	WorktreeDirectory string `json:"worktreeDirectory"`
	ReviewSHA256      string `json:"reviewSha256"`
}

type HandoffStatus struct {
	ProtocolVersion      int             `json:"protocolVersion"`
	Status               string          `json:"status"`
	RequestID            string          `json:"requestId,omitempty"`
	ReviewSHA256         string          `json:"reviewSha256,omitempty"`
	Repository           string          `json:"repository,omitempty"`
	DeploymentRepository string          `json:"deploymentRepository,omitempty"`
	ApplicationCommit    string          `json:"applicationCommit,omitempty"`
	PackManifestSHA256   string          `json:"packManifestSha256,omitempty"`
	ModelPath            string          `json:"modelPath,omitempty"`
	ModelSHA256          string          `json:"modelSha256,omitempty"`
	InstallationStatus   string          `json:"installationStatus,omitempty"`
	SourceDirectory      string          `json:"sourceDirectory,omitempty"`
	WorktreeDirectory    string          `json:"worktreeDirectory,omitempty"`
	Files                []FileOperation `json:"files,omitempty"`
	Warnings             []string        `json:"warnings"`
}

func handoffHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".radpack", "handoffs"), nil
}

func commonDirectory(ctx context.Context, root string) (string, error) {
	raw, err := gitText(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return canonicalDirectory(raw)
}

func ensurePrivateDirectory(root string) error {
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	return validatePrivateDirectory(root)
}

func validatePrivateDirectory(root string) error {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	// Reject a redirected state path rather than following a symlink.
	for p := absolute; p != filepath.Dir(p); p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("Radpack handoff state must use real directories")
		}
	}
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("Radpack handoff state must not be writable by other users")
	}
	return nil
}

func savePrivateJSON(root, file string, value any) error {
	data, err := jsonBytes(value)
	if err != nil {
		return err
	}
	if err := writeExclusive(root, file, data); err != nil {
		return err
	}
	return os.Chmod(filepath.Join(root, file), 0600)
}

func PrepareDesktopInstall(ctx context.Context, packDirectory, destination string, o InstallOptions) (resultPath string, resultErr error) {
	pack, err := LoadPack(packDirectory)
	if err != nil {
		return "", err
	}
	if o.SelectedCommit == "" {
		o.SelectedCommit = pack.Manifest.Upstream.Commit
	}
	if !commitPattern.MatchString(o.SelectedCommit) {
		return "", fmt.Errorf("invalid selected commit")
	}
	home, err := handoffHome()
	if err != nil {
		return "", err
	}
	if err := ensurePrivateDirectory(home); err != nil {
		return "", err
	}
	candidates := filepath.Join(filepath.Dir(home), "candidates")
	if err := ensurePrivateDirectory(candidates); err != nil {
		return "", err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	requestID := hex.EncodeToString(random)
	// Git discovers .git/HEAD before ordinary file operations. Keep the staged
	// repository out of the deep clone-key directory so Windows can discover it.
	candidate := filepath.Join(candidates, requestID)
	// Stage and validate the complete resulting application before publishing
	// a handoff. This shares the exact installer used by --no-open.
	stage, err := os.MkdirTemp(home, ".prepare-")
	if err != nil {
		return "", err
	}
	published := false
	candidateCreated := false
	defer func() {
		if !published {
			if err := os.RemoveAll(stage); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("handoff staging cleanup failed: %w", err))
			}
			if candidateCreated {
				if err := os.RemoveAll(candidate); err != nil {
					resultErr = errors.Join(resultErr, fmt.Errorf("candidate cleanup failed: %w", err))
				}
			}
		}
	}()
	if _, err := InstallPack(ctx, packDirectory, candidate, o); err != nil {
		return "", err
	}
	candidateCreated = true
	result, err := VerifyInstallation(ctx, "", candidate)
	if err != nil {
		return "", err
	}
	return withNewDirectory(destination, func(source string) error {
		if err := cloneRevision(ctx, source, pack.Manifest.Upstream.Repository, o.SelectedCommit); err != nil {
			return err
		}
		canonical, err := canonicalDirectory(source)
		if err != nil {
			return err
		}
		common, err := commonDirectory(ctx, canonical)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		record := handoffRecord{1, requestID, canonical, common, o.SelectedCommit, pack.Digest,
			result.Status, now.Format(time.RFC3339), now.Add(7 * 24 * time.Hour).Format(time.RFC3339)}
		// A path can later be reused for a different clone. Keep the request ID
		// in Git's local metadata so an old request cannot bind to that new clone.
		if err := writeExclusive(common, "radpack-handoff-id", []byte(record.RequestID)); err != nil {
			return err
		}
		if err := savePrivateJSON(stage, "request.json", record); err != nil {
			return err
		}
		final := filepath.Join(home, hash([]byte(common)))
		// Do not replace a prior request, even if its source directory was removed
		// and a new clone was later created with the same path.
		if _, err := os.Lstat(final); err == nil {
			return fmt.Errorf("a handoff already exists for this checkout path; use another destination")
		}
		if err := os.Rename(stage, final); err != nil {
			return err
		}
		published = true
		return nil
	})
}

type loadedHandoff struct {
	directory string
	candidate string
	record    handoffRecord
	pack      *Pack
	receipt   Receipt
	status    HandoffStatus
}

func loadHandoff(ctx context.Context, directory string) (*loadedHandoff, error) {
	root, err := repositoryRoot(ctx, directory)
	if err != nil {
		return nil, err
	}
	common, err := commonDirectory(ctx, root)
	if err != nil {
		return nil, err
	}
	home, err := handoffHome()
	if err != nil {
		return nil, err
	}
	state := filepath.Join(home, hash([]byte(common)))
	info, err := os.Lstat(state)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("invalid handoff state directory")
	}
	if err := validatePrivateDirectory(state); err != nil {
		return nil, err
	}
	request, err := readRegular(state, "request.json")
	if err != nil {
		return nil, err
	}
	if request == nil {
		return nil, fmt.Errorf("handoff request is missing")
	}
	var record handoffRecord
	if err := decodeStrict(request.Bytes, &record); err != nil {
		return nil, err
	}
	if record.ProtocolVersion != 1 || !match(`^[a-f0-9]{32}$`, record.RequestID) || record.CommonDirectory != common ||
		!commitPattern.MatchString(record.ApplicationCommit) || !hashPattern.MatchString(record.PackManifestSHA256) {
		return nil, fmt.Errorf("invalid or mismatched handoff request")
	}
	source, err := canonicalDirectory(record.SourceDirectory)
	if err != nil {
		return nil, err
	}
	sourceCommon, err := commonDirectory(ctx, source)
	if err != nil {
		return nil, err
	}
	if sourceCommon != common {
		return nil, fmt.Errorf("source clone identity changed")
	}
	identity, err := readRegular(common, "radpack-handoff-id")
	if err != nil {
		return nil, err
	}
	if identity == nil || string(identity.Bytes) != record.RequestID {
		return nil, fmt.Errorf("source clone handoff identity changed")
	}
	candidate := filepath.Join(filepath.Dir(home), "candidates", record.RequestID)
	if err := validatePrivateDirectory(candidate); err != nil {
		return nil, err
	}
	pack, err := LoadPack(filepath.Join(candidate, ".radpack", "pack"))
	if err != nil {
		return nil, err
	}
	if pack.Digest != record.PackManifestSHA256 {
		return nil, fmt.Errorf("handoff pack has changed")
	}
	if _, err := VerifyInstallation(ctx, "", candidate); err != nil {
		return nil, fmt.Errorf("staged installation failed verification: %w", err)
	}
	receiptFile, err := readRegular(candidate, receiptPath)
	if err != nil {
		return nil, err
	}
	if receiptFile == nil {
		return nil, fmt.Errorf("staged receipt disappeared")
	}
	var receipt Receipt
	if err := decodeStrict(receiptFile.Bytes, &receipt); err != nil {
		return nil, err
	}
	if receipt.ApplicationCommit != record.ApplicationCommit || receipt.Status != record.InstallationStatus {
		return nil, fmt.Errorf("handoff snapshot identity changed")
	}
	status := HandoffStatus{ProtocolVersion: 1, Status: "pending", RequestID: record.RequestID, Repository: pack.Manifest.Upstream.Repository,
		ApplicationCommit: record.ApplicationCommit, PackManifestSHA256: pack.Digest, ModelPath: pack.Manifest.Application,
		InstallationStatus: record.InstallationStatus, SourceDirectory: source, WorktreeDirectory: root,
		Files: []FileOperation{}, Warnings: []string{}}
	selectedTree, err := treeAt(ctx, candidate, record.ApplicationCommit)
	if err != nil {
		return nil, err
	}
	for _, file := range receipt.Files {
		original, err := baseFile(ctx, candidate, selectedTree, file.Path)
		if err != nil {
			return nil, err
		}
		before := fingerprint(original)
		if before != nil || file.After != nil {
			operation := "replace"
			switch {
			case before == nil:
				operation = "add"
			case file.After == nil:
				operation = "delete"
			case same(before, file.After):
				operation = "retain"
			}
			status.Files = append(status.Files, FileOperation{file.Path, operation, before, file.After})
		}
		if file.Path == status.ModelPath && file.After != nil {
			status.ModelSHA256 = file.After.SHA256
		}
		_, evidence, evidenceErr := detectRadiusVersion(candidate, status.ModelPath, pack.Manifest.RadiusVersion)
		if evidenceErr != nil {
			status.Warnings = append(status.Warnings, "Model version evidence could not be inspected; review it before deploying.")
		} else {
			status.Warnings = append(status.Warnings, evidence.Warnings...)
		}
		assets := map[string][]byte{}
		for _, file := range receipt.Files {
			if file.After == nil || !strings.HasSuffix(file.Path, ".bicep") {
				continue
			}
			payload, err := readRegular(candidate, file.Path)
			if err != nil {
				return nil, err
			}
			if payload != nil {
				assets[file.Path] = payload.Bytes
			}
		}
		reviewManifest := pack.Manifest
		reviewManifest.Upstream.Commit = record.ApplicationCommit
		reviewManifest.Files = status.Files
		status.Warnings = append(status.Warnings, buildSourceWarnings(reviewManifest, assets)...)
	}
	if record.InstallationStatus == "adaptation-required" {
		status.Warnings = append(status.Warnings, "Latest-source integration requires adaptation; this is not a validated graph of the selected application.")
	}
	loaded := &loadedHandoff{state, candidate, record, pack, receipt, status}
	if status.ModelSHA256 == "" {
		loaded.status.Status = "blocked"
		loaded.status.Warnings = append(status.Warnings, "The prepared latest-source snapshot has no application model. Use --no-open and resolve the adaptation before opening a graph.")
		return loaded, nil
	}
	head, err := gitText(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	if head != record.ApplicationCommit {
		loaded.status.Status = "blocked"
		loaded.status.Warnings = append(status.Warnings, "Session checkout is not at the prepared application commit. Create a session at the recorded revision; no files were changed.")
		return loaded, nil
	}
	if err := verifyApplicationOrigin(ctx, root, pack.Manifest.Upstream.Repository); err != nil {
		return nil, err
	}
	binding, err := readDeploymentBinding(ctx, root)
	if err != nil {
		return nil, err
	}
	if binding != nil {
		loaded.status.DeploymentRepository = binding.Repository
	}
	claimFile, err := readRegular(state, "claim.json")
	if err != nil {
		return nil, err
	}
	if claimFile != nil {
		var claim handoffClaim
		if err := decodeStrict(claimFile.Bytes, &claim); err != nil {
			return nil, err
		}
		if !hashPattern.MatchString(claim.ReviewSHA256) {
			return nil, fmt.Errorf("handoff claim has no valid approval fingerprint")
		}
		if claim.RequestID != record.RequestID || claim.WorktreeDirectory != root {
			loaded.status.Status = "blocked"
			loaded.status.Warnings = append(status.Warnings, "This handoff was claimed by another worktree.")
			return loaded, nil
		}
		if _, err := VerifyInstallation(ctx, filepath.Join(candidate, ".radpack", "pack"), root); err == nil {
			loaded.status.Status = "applied"
			loaded.status.ReviewSHA256 = claim.ReviewSHA256
			return loaded, nil
		}
		// An interrupted partial write is never retried automatically.
		loaded.status.Status = "blocked"
		loaded.status.Warnings = append(status.Warnings, "A previous application attempt is incomplete or has been modified. Inspect the worktree; do not retry blindly.")
		return loaded, nil
	}
	expires, err := time.Parse(time.RFC3339, record.ExpiresAt)
	if err != nil || time.Now().After(expires) {
		loaded.status.Status = "blocked"
		loaded.status.Warnings = append(status.Warnings, "This handoff has expired. Prepare a fresh installation in a new directory.")
		return loaded, nil
	}
	if err := cleanWorktree(ctx, root); err != nil {
		loaded.status.Status = "blocked"
		loaded.status.Warnings = append(status.Warnings, "Session checkout has tracked, untracked, or ignored changes. Preserve them before applying a pack.")
		return loaded, nil
	}
	review, err := jsonBytes(loaded.status)
	if err != nil {
		return nil, err
	}
	loaded.status.ReviewSHA256 = hash(review)
	return loaded, nil
}

func cleanWorktree(ctx context.Context, root string) error {
	for _, args := range [][]string{
		{"diff", "--no-ext-diff", "--no-textconv", "--name-only", "HEAD", "--"},
		{"ls-files", "--others", "-z"},
	} {
		b, err := git(ctx, root, args...)
		if err != nil {
			return err
		}
		if len(b) > 0 {
			return fmt.Errorf("worktree is not clean")
		}
	}
	return nil
}

func HandoffStatusFor(ctx context.Context, directory string) (HandoffStatus, error) {
	h, err := loadHandoff(ctx, directory)
	if err != nil {
		return HandoffStatus{}, err
	}
	if h == nil {
		return HandoffStatus{ProtocolVersion: 1, Status: "none", Warnings: []string{}}, nil
	}
	return h.status, nil
}

// ApplyHandoff requires consent bound to this precise worktree and review.
// It never removes a worktree on error; a claimed partial attempt stays blocked.
func ApplyHandoff(ctx context.Context, directory, requestID, approval string) (HandoffStatus, error) {
	h, err := loadHandoff(ctx, directory)
	if err != nil {
		return HandoffStatus{}, err
	}
	if h == nil {
		return HandoffStatus{}, fmt.Errorf("no pending handoff for this checkout")
	}
	if requestID != h.record.RequestID {
		return HandoffStatus{}, fmt.Errorf("handoff request ID differs")
	}
	if h.status.Status == "applied" {
		return h.status, nil
	}
	if h.status.Status != "pending" {
		return h.status, fmt.Errorf("handoff is blocked; no files changed")
	}
	if !hashPattern.MatchString(approval) || approval != h.status.ReviewSHA256 {
		return h.status, fmt.Errorf("handoff approval does not match this worktree review")
	}
	root := h.status.WorktreeDirectory
	if root == h.record.SourceDirectory {
		return h.status, fmt.Errorf("apply requires an app-created worktree, not the original source clone")
	}
	// Git itself must report this path as a linked worktree of the source clone.
	gitDir, err := gitText(ctx, root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return h.status, err
	}
	actual, err := canonicalDirectory(gitDir)
	if err != nil {
		return h.status, err
	}
	if actual == h.record.CommonDirectory {
		return h.status, fmt.Errorf("checkout is not a linked worktree")
	}
	if err := ctx.Err(); err != nil {
		return h.status, err
	}
	if err := savePrivateJSON(h.directory, "claim.json", handoffClaim{requestID, root, approval}); err != nil {
		return h.status, fmt.Errorf("handoff already claimed or claim could not be saved: %w", err)
	}
	candidate := h.candidate
	tree, err := treeAt(ctx, root, h.record.ApplicationCommit)
	if err != nil {
		return h.status, err
	}
	// Recheck after the exclusive claim; refuse setup-script or user changes.
	if err := cleanWorktree(ctx, root); err != nil {
		return h.status, err
	}
	for _, file := range h.receipt.Files {
		if err := ctx.Err(); err != nil {
			return h.status, err
		}
		before, err := baseFile(ctx, root, tree, file.Path)
		if err != nil {
			return h.status, err
		}
		current, err := readRegular(root, file.Path)
		if err != nil {
			return h.status, err
		}
		if !same(fingerprint(before), fingerprint(current)) {
			return h.status, fmt.Errorf("worktree changed before writing %s", file.Path)
		}
		if same(fingerprint(current), file.After) {
			continue
		}
		if file.After == nil {
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(file.Path))); err != nil {
				return h.status, err
			}
			continue
		}
		payload, err := readRegular(candidate, file.Path)
		if err != nil {
			return h.status, err
		}
		if payload == nil || hash(payload.Bytes) != file.After.SHA256 ||
			(runtime.GOOS != "windows" && !reflect.DeepEqual(fingerprint(payload), file.After)) {
			return h.status, fmt.Errorf("staged payload changed")
		}
		if current == nil {
			if err := writeExclusive(root, file.Path, payload.Bytes); err != nil {
				return h.status, err
			}
		} else if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(file.Path)), payload.Bytes, 0644); err != nil {
			return h.status, err
		}
		mode := os.FileMode(0644)
		if file.After.Mode == "100755" {
			mode = 0755
		}
		if err := os.Chmod(filepath.Join(root, filepath.FromSlash(file.Path)), mode); err != nil {
			return h.status, err
		}
	}
	for _, file := range h.receipt.Metadata {
		payload, err := readRegular(candidate, file.Path)
		if err != nil {
			return h.status, err
		}
		if payload == nil || hash(payload.Bytes) != file.SHA256 {
			return h.status, fmt.Errorf("staged metadata changed")
		}
		if err := writeExclusive(root, file.Path, payload.Bytes); err != nil {
			return h.status, err
		}
	}
	receipt, err := readRegular(candidate, receiptPath)
	if err != nil {
		return h.status, err
	}
	if receipt == nil {
		return h.status, fmt.Errorf("staged receipt disappeared")
	}
	if err := writeExclusive(root, receiptPath, receipt.Bytes); err != nil {
		return h.status, err
	}
	if _, err := VerifyInstallation(ctx, "", root); err != nil {
		return h.status, err
	}
	h.status.Status = "applied"
	h.status.ReviewSHA256 = approval
	return h.status, nil
}

func launchCopilot(ctx context.Context, directory string) error {
	executable := strings.TrimSpace(os.Getenv("RADPACK_GITHUB_COMMAND"))
	if executable != "" && !filepath.IsAbs(executable) {
		return fmt.Errorf("RADPACK_GITHUB_COMMAND must be an absolute executable path")
	}
	return runCopilotOpen(ctx, executable, directory, true)
}

func resumeDesktop(ctx context.Context, directory string) error {
	h, err := loadHandoff(ctx, directory)
	if err != nil {
		return err
	}
	if h == nil {
		return fmt.Errorf("no Radpack desktop handoff is registered for this clone")
	}
	target := h.record.SourceDirectory
	claimFile, err := readRegular(h.directory, "claim.json")
	if err != nil {
		return err
	}
	if claimFile != nil {
		var claim handoffClaim
		if err := decodeStrict(claimFile.Bytes, &claim); err != nil {
			return err
		}
		if claim.RequestID != h.record.RequestID {
			return fmt.Errorf("handoff claim identity differs")
		}
		claimed, err := loadHandoff(ctx, claim.WorktreeDirectory)
		if err != nil {
			return err
		}
		if claimed == nil || claimed.status.Status != "applied" {
			return fmt.Errorf("claimed handoff is incomplete or modified; inspect its worktree instead of starting another session")
		}
		h = claimed
		target = claimed.status.WorktreeDirectory
	}
	if h.status.Status == "blocked" {
		return fmt.Errorf("handoff is blocked: %s", strings.Join(h.status.Warnings, " "))
	}
	executable := strings.TrimSpace(os.Getenv("RADPACK_GITHUB_COMMAND"))
	if executable != "" && !filepath.IsAbs(executable) {
		return fmt.Errorf("RADPACK_GITHUB_COMMAND must be an absolute executable path")
	}
	// Unlike initial launch, resume allows the app to reuse an existing session.
	return runCopilotOpen(ctx, executable, target, false)
}
