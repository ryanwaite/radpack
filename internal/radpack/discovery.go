package radpack

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	bundle "github.com/ryanwaite/radpack"
)

type assetManifest struct {
	FormatVersion int      `json:"formatVersion"`
	Kind          string   `json:"kind"`
	Name          string   `json:"name"`
	Application   string   `json:"application"`
	RadiusVersion *string  `json:"radiusVersion"`
	Assets        []string `json:"assets"`
}
type catalog struct {
	FormatVersion int               `json:"formatVersion"`
	Applications  map[string]string `json:"applications"`
}
type packLocation struct{ Repository, Ref, Directory string }
type resolvedPack struct {
	Directory string
	Source    PackSource
}
type ApplicationOptions struct {
	Directory, Pack, PackPath string
	Latest, Adapt             bool
	Catalog                   []byte
	VersionProbe              radiusVersionProbe
}
type ApplicationResult struct {
	Directory, Status, ReferenceCommit, SelectedCommit string
	PackSource                                         PackSource
}

func parsePackURL(value string) (packLocation, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return packLocation{}, fmt.Errorf("pack override must be a local directory or credential-free GitHub tree URL")
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(parts) < 5 || parts[2] != "tree" {
		return packLocation{}, fmt.Errorf("pack URL must be https://github.com/owner/repo/tree/<ref>/<directory>")
	}
	for i, part := range parts {
		parts[i], err = url.PathUnescape(part)
		if err != nil {
			return packLocation{}, fmt.Errorf("invalid pack URL encoding")
		}
	}
	repo, err := repositoryURL(parts[0] + "/" + parts[1])
	if err != nil {
		return packLocation{}, err
	}
	ref := parts[3]
	if !match(`^[A-Za-z0-9][A-Za-z0-9._-]*$`, ref) || strings.Contains(ref, "..") {
		return packLocation{}, fmt.Errorf("use a single-segment ref or exact SHA in a pack URL")
	}
	dir := strings.Join(parts[4:], "/")
	if err := safePath(dir); err != nil {
		return packLocation{}, err
	}
	return packLocation{repo, ref, dir}, nil
}
func localDirectory(value string) (bool, error) {
	// URLs are never filesystem paths (notably on Windows).
	if strings.Contains(value, "://") {
		return false, nil
	}
	info, err := os.Lstat(value)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("pack directory cannot be a symbolic link")
	}
	return info.IsDir(), nil
}
func repositoryPack(ctx context.Context, checkout, commit, repo, manifestPath, temp string) (*resolvedPack, error) {
	file, err := readRegular(checkout, manifestPath)
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, nil
	}
	var descriptor assetManifest
	if err := decodeStrict(file.Bytes, &descriptor); err != nil {
		return nil, err
	}
	if descriptor.FormatVersion != 1 || descriptor.Kind != "in-repository" || len(descriptor.Assets) > 1000 {
		return nil, fmt.Errorf("invalid in-repository manifest")
	}
	hasApp := false
	for _, asset := range descriptor.Assets {
		if err := safePath(asset); err != nil {
			return nil, err
		}
		if asset == descriptor.Application {
			hasApp = true
		}
	}
	if !hasApp {
		return nil, fmt.Errorf("assets must contain the application")
	}
	p, err := PlanCreate(ctx, CreateOptions{Source: checkout, Base: commit, Upstream: repo, Name: descriptor.Name,
		RadiusVersion: descriptor.RadiusVersion, Include: descriptor.Assets})
	if err != nil {
		return nil, err
	}
	if p.Manifest.Application != descriptor.Application {
		return nil, fmt.Errorf("manifest application does not match discovered model")
	}
	if len(p.Blockers) > 0 {
		return nil, fmt.Errorf("in-repository assets are not portable: %s", strings.Join(p.Blockers, "; "))
	}
	dir, err := WritePack(p, filepath.Join(temp, "materialized-pack"), p.Approval)
	if err != nil {
		return nil, err
	}
	return &resolvedPack{dir, PackSource{"in-repository", repo, commit, manifestPath, hash(file.Bytes)}}, nil
}
func resolveOverride(ctx context.Context, value, temp string) (*resolvedPack, error) {
	local, err := localDirectory(value)
	if err != nil {
		return nil, err
	}
	if local {
		p, err := LoadPack(value)
		if err != nil {
			return nil, err
		}
		return &resolvedPack{value, PackSource{Kind: "local", ManifestSHA256: p.Digest}}, nil
	}
	if strings.HasPrefix(value, ".") || filepath.IsAbs(value) {
		return nil, fmt.Errorf("local pack override directory does not exist")
	}
	loc, err := parsePackURL(value)
	if err != nil {
		return nil, err
	}
	commit, err := resolveRevision(ctx, loc.Repository, loc.Ref)
	if err != nil {
		return nil, err
	}
	checkout := filepath.Join(temp, "pack-source")
	if err := os.Mkdir(checkout, 0755); err != nil {
		return nil, err
	}
	if err := cloneRevision(ctx, checkout, loc.Repository, commit); err != nil {
		return nil, err
	}
	file, err := readRegular(checkout, loc.Directory+"/radpack.json")
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, fmt.Errorf("pack URL does not contain radpack.json")
	}
	dir := filepath.Join(checkout, filepath.FromSlash(loc.Directory))
	p, err := LoadPack(dir)
	if err != nil {
		return nil, err
	}
	return &resolvedPack{dir, PackSource{"github", loc.Repository, commit, loc.Directory, p.Digest}}, nil
}
func InstallApplication(ctx context.Context, target string, o ApplicationOptions, report func(string)) (result *ApplicationResult, err error) {
	if o.Latest != o.Adapt {
		return nil, fmt.Errorf("--latest and --adapt must be used together")
	}
	if o.Pack != "" && o.PackPath != "" {
		return nil, fmt.Errorf("use --pack or --pack-path, not both")
	}
	if o.PackPath != "" {
		if err := safePath(o.PackPath); err != nil {
			return nil, err
		}
	}
	local, err := localDirectory(target)
	if err != nil {
		return nil, err
	}
	if local && (o.Pack != "" || o.PackPath != "") {
		return nil, fmt.Errorf("pack overrides require an application repository target")
	}
	if !local && (strings.HasPrefix(target, ".") || filepath.IsAbs(target)) {
		return nil, fmt.Errorf("local pack directory does not exist")
	}
	temp, err := os.MkdirTemp("", "radpack-discovery-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if e := os.RemoveAll(temp); e != nil {
			err = errors.Join(err, fmt.Errorf("discovery cleanup failed: %w", e))
		}
	}()
	var repo, head string
	var resolved *resolvedPack
	if local {
		p, e := LoadPack(target)
		if e != nil {
			return nil, e
		}
		repo = p.Manifest.Upstream.Repository
		resolved = &resolvedPack{target, PackSource{Kind: "local", ManifestSHA256: p.Digest}}
	} else {
		repo, err = repositoryURL(target)
		if err != nil {
			return nil, err
		}
		if o.Pack != "" {
			resolved, err = resolveOverride(ctx, o.Pack, temp)
			if err != nil {
				return nil, err
			}
		} else {
			head, err = resolveRevision(ctx, repo, "HEAD")
			if err != nil {
				return nil, err
			}
			checkout := filepath.Join(temp, "application")
			if err := os.Mkdir(checkout, 0755); err != nil {
				return nil, err
			}
			if err := cloneRevision(ctx, checkout, repo, head); err != nil {
				return nil, err
			}
			location := ".radpack/radpack.json"
			if o.PackPath != "" {
				location = o.PackPath + "/radpack.json"
			}
			resolved, err = repositoryPack(ctx, checkout, head, repo, location, temp)
			if err != nil {
				return nil, err
			}
			if resolved == nil && o.PackPath != "" {
				return nil, fmt.Errorf("no manifest found at --pack-path; catalog fallback is disabled")
			}
			if resolved == nil {
				data := o.Catalog
				if data == nil {
					data = bundle.Catalog
				}
				var c catalog
				if err := decodeStrict(data, &c); err != nil {
					return nil, err
				}
				if c.FormatVersion != 1 || c.Applications == nil {
					return nil, fmt.Errorf("invalid Radpack catalog")
				}
				key := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(repo, "https://github.com/"), ".git"))
				entry := c.Applications[key]
				if entry == "" {
					return nil, fmt.Errorf("no Radpack found for %s; add .radpack/radpack.json upstream or supply --pack", key)
				}
				loc, err := parsePackURL(entry)
				if err != nil {
					return nil, err
				}
				if !commitPattern.MatchString(loc.Ref) {
					return nil, fmt.Errorf("catalog entries must pin the pack repository to a full commit SHA")
				}
				resolved, err = resolveOverride(ctx, entry, temp)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	p, err := LoadPack(resolved.Directory)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(p.Manifest.Upstream.Repository, repo) {
		return nil, fmt.Errorf("selected pack belongs to a different application repository")
	}
	selected := p.Manifest.Upstream.Commit
	if o.Latest {
		if head == "" {
			head, err = resolveRevision(ctx, repo, "HEAD")
			if err != nil {
				return nil, err
			}
		}
		selected = head
	}
	if o.Directory == "" {
		o.Directory = strings.TrimSuffix(path.Base(repo), ".git")
	}
	if report != nil {
		report(fmt.Sprintf("Application: %s\nReference commit: %s\nSelected commit: %s", repo, p.Manifest.Upstream.Commit, selected))
		b, e := jsonBytes(resolved.Source)
		if e != nil {
			return nil, e
		}
		report("Pack source: " + strings.TrimSpace(string(b)) + "\nOnly install packs from publishers you trust.")
	}
	output, err := InstallPack(ctx, resolved.Directory, o.Directory, InstallOptions{selected, o.Adapt, resolved.Source})
	if err != nil {
		return nil, err
	}
	if report != nil {
		_, evidence, versionErr := detectRadiusVersion(output, p.Manifest.Application, p.Manifest.RadiusVersion)
		if versionErr != nil {
			report("Warning: Could not inspect installed model version evidence. Installation is complete; review the model before deploying.")
		} else {
			for _, warning := range evidence.Warnings {
				report("Warning: " + warning)
			}
		}
		if o.VersionProbe != nil {
			for _, warning := range radiusInstallWarnings(ctx, p.Manifest.RadiusVersion, o.VersionProbe) {
				report("Warning: " + warning)
			}
		}
	}
	status := "installed"
	if o.Adapt {
		status = "adaptation-required"
	}
	return &ApplicationResult{output, status, p.Manifest.Upstream.Commit, selected, resolved.Source}, nil
}
