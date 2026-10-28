package radpack

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

func validateManifest(m Manifest) error {
	if m.FormatVersion != 1 {
		return fmt.Errorf("unsupported pack format version")
	}
	if !namePattern.MatchString(m.Name) {
		return fmt.Errorf("invalid pack name")
	}
	url, err := repositoryURL(m.Upstream.Repository)
	if err != nil || url != m.Upstream.Repository || !commitPattern.MatchString(m.Upstream.Commit) {
		return fmt.Errorf("upstream must have a canonical URL and full commit SHA")
	}
	if m.RadiusVersion != nil && !versionPattern.MatchString(*m.RadiusVersion) {
		return fmt.Errorf("Radius version must be an exact release version or null")
	}
	if m.Application != "app.bicep" && m.Application != ".radius/app.bicep" {
		return fmt.Errorf("application must be app.bicep or .radius/app.bicep")
	}
	if len(m.Files) == 0 || len(m.Files) > 1000 {
		return fmt.Errorf("a pack must declare between 1 and 1000 files")
	}
	seen := map[string]bool{}
	hasApp := false
	for _, file := range m.Files {
		reason, err := excludedReason(file.Path)
		if err != nil {
			return err
		}
		if reason != "" {
			return fmt.Errorf("forbidden pack path %s: %s", file.Path, reason)
		}
		key := strings.ToLower(file.Path)
		if seen[key] {
			return fmt.Errorf("duplicate or case-colliding path: %s", file.Path)
		}
		seen[key] = true
		if err := validateFingerprint(file.Before); err != nil {
			return err
		}
		if err := validateFingerprint(file.After); err != nil {
			return err
		}
		valid := false
		switch file.Operation {
		case "add":
			valid = file.Before == nil && file.After != nil
		case "delete":
			valid = file.Before != nil && file.After == nil
		case "replace":
			valid = file.Before != nil && file.After != nil && !same(file.Before, file.After)
		case "retain":
			valid = file.Before != nil && same(file.Before, file.After)
		}
		if !valid {
			return fmt.Errorf("invalid operation for %s", file.Path)
		}
		if file.Path == m.Application && file.After != nil {
			hasApp = true
		}
	}
	for file := range seen {
		for parent := path.Dir(file); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return fmt.Errorf("file paths overlap a parent file")
			}
		}
	}
	if !hasApp {
		return fmt.Errorf("pack does not contain its application definition")
	}
	return nil
}
func baseFile(ctx context.Context, source string, tree map[string]treeEntry, file string) (*regularFile, error) {
	entry, ok := tree[file]
	if !ok {
		return nil, nil
	}
	if entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") {
		return nil, fmt.Errorf("cannot package a symbolic link or submodule: %s", file)
	}
	b, err := git(ctx, source, "cat-file", "blob", entry.Object)
	if err != nil {
		return nil, err
	}
	return &regularFile{b, entry.Mode}, nil
}
func nearestConfig(source, file string) (string, error) {
	for dir := path.Dir(file); ; dir = path.Dir(dir) {
		candidate := path.Join(dir, "bicepconfig.json")
		f, err := readRegular(source, candidate)
		if err != nil {
			return "", err
		}
		if f != nil {
			return candidate, nil
		}
		if dir == "." {
			return "", nil
		}
	}
}
func canonicalDirectory(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}
func repositoryRoot(ctx context.Context, dir string) (string, error) {
	root, err := canonicalDirectory(dir)
	if err != nil {
		return "", err
	}
	top, err := gitText(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	top, err = canonicalDirectory(top)
	if err != nil {
		return "", err
	}
	if root != top {
		return "", fmt.Errorf("directory must be the Git repository root")
	}
	return root, nil
}
func radiusFiles(source string) ([]string, error) {
	result := []string{}
	var walk func(string) error
	walk = func(relative string) error {
		info, err := os.Lstat(filepath.Join(source, filepath.FromSlash(relative)))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not allowed: %s", relative)
		}
		entries, err := os.ReadDir(filepath.Join(source, filepath.FromSlash(relative)))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			file := relative + "/" + entry.Name()
			if len(result) >= 1000 {
				return fmt.Errorf("too many Radius files to inspect")
			}
			reason, err := excludedReason(file)
			if err != nil || reason != "" || !entry.IsDir() {
				result = append(result, file)
				continue
			}
			if err := walk(file); err != nil {
				return err
			}
		}
		return nil
	}
	err := walk(".radius")
	return result, err
}

type CreateOptions struct {
	Source, Base, Upstream, Name string
	RadiusVersion                *string
	Include                      []string
}

func PlanCreate(ctx context.Context, o CreateOptions) (*Plan, error) {
	if o.Source == "" {
		o.Source = "."
	}
	source, err := repositoryRoot(ctx, o.Source)
	if err != nil {
		return nil, err
	}
	if !commitPattern.MatchString(o.Base) {
		return nil, fmt.Errorf("--base must be the full 40-character upstream commit SHA")
	}
	resolved, err := gitText(ctx, source, "rev-parse", "--verify", o.Base+"^{commit}")
	if err != nil {
		return nil, err
	}
	if resolved != o.Base {
		return nil, fmt.Errorf("baseline is not a commit")
	}
	if o.Upstream == "" {
		o.Upstream, err = gitText(ctx, source, "config", "--get", "remote.origin.url")
		if err != nil {
			return nil, err
		}
	}
	repository, err := repositoryURL(o.Upstream)
	if err != nil {
		return nil, err
	}
	name := o.Name
	if name == "" {
		name = strings.TrimSuffix(path.Base(repository), ".git")
		name = strings.ToLower(name)
		name = regexpName(name)
	}
	if !namePattern.MatchString(name) {
		return nil, fmt.Errorf("invalid pack name")
	}
	app := ".radius/app.bicep"
	f, err := readRegular(source, app)
	if err != nil {
		return nil, err
	}
	if f == nil {
		app = "app.bicep"
		f, err = readRegular(source, app)
		if err != nil {
			return nil, err
		}
	}
	if f == nil {
		return nil, fmt.Errorf("no application model found; prepare the application with Radius first")
	}
	selected := []string{}
	seen := map[string]bool{}
	add := func(file string) {
		if !seen[file] {
			seen[file] = true
			selected = append(selected, file)
		}
	}
	add(app)
	for _, asset := range []string{"app.origin.json", "custom-types.yaml", "custom-recipe-pack.bicep"} {
		file := path.Join(path.Dir(app), asset)
		f, err := readRegular(source, file)
		if err != nil {
			return nil, err
		}
		if f != nil {
			add(file)
		}
	}
	for _, file := range o.Include {
		if err := safePath(file); err != nil {
			return nil, err
		}
		add(file)
	}
	tree, err := treeAt(ctx, source, o.Base)
	if err != nil {
		return nil, err
	}
	p := &Plan{Review: Review{
		Manifest: Manifest{1, name, Upstream{repository, o.Base}, app, o.RadiusVersion, []FileOperation{}},
		Excluded: []Excluded{}, Unselected: []string{}, Blockers: []string{},
	}, Payloads: map[string][]byte{}}
	total := 0
	for i := 0; i < len(selected); i++ {
		if len(selected) > 1000 {
			return nil, fmt.Errorf("too many selected files")
		}
		file := selected[i]
		err := func() error {
			reason, err := excludedReason(file)
			if err != nil {
				return err
			}
			if reason != "" {
				return fmt.Errorf("cannot package %s: %s", file, reason)
			}
			original, err := baseFile(ctx, source, tree, file)
			if err != nil {
				return err
			}
			current, err := readRegular(source, file)
			if err != nil {
				return err
			}
			if current == nil && original == nil {
				return fmt.Errorf("selected file does not exist: %s", file)
			}
			if current != nil {
				if err := assertPortableFile(current.Bytes, file); err != nil {
					return err
				}
				deps, err := dependencies(current.Bytes, file)
				if err != nil {
					return err
				}
				for _, dep := range deps {
					f, err := readRegular(source, dep)
					if err != nil {
						return err
					}
					if f == nil {
						return fmt.Errorf("missing local dependency of %s: %s", file, dep)
					}
					add(dep)
				}
				if strings.HasSuffix(file, ".bicep") {
					config, err := nearestConfig(source, file)
					if err != nil {
						return err
					}
					if config != "" {
						add(config)
					}
				}
				total += len(current.Bytes)
				if total > maxPackBytes {
					return fmt.Errorf("selected files exceed the 32 MiB pack limit")
				}
			}
			before, after := fingerprint(original), fingerprint(current)
			op := "replace"
			switch {
			case original == nil:
				op = "add"
			case current == nil:
				op = "delete"
			case same(before, after):
				op = "retain"
			}
			p.Manifest.Files = append(p.Manifest.Files, FileOperation{file, op, before, after})
			if current != nil && op != "retain" {
				p.Payloads[file] = current.Bytes
			}
			return nil
		}()
		if err != nil {
			p.Blockers = append(p.Blockers, err.Error())
		}
	}
	sort.Slice(p.Manifest.Files, func(i, j int) bool { return p.Manifest.Files[i].Path < p.Manifest.Files[j].Path })
	if len(p.Blockers) == 0 {
		if err := validateManifest(p.Manifest); err != nil {
			return nil, err
		}
	}
	candidates := map[string]bool{}
	for _, args := range [][]string{
		{"diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-only", "-z", o.Base, "--"},
		{"ls-files", "--others", "--exclude-standard", "-z"},
	} {
		b, err := git(ctx, source, args...)
		if err != nil {
			return nil, err
		}
		for _, file := range splitNull(b) {
			candidates[file] = true
		}
	}
	radius, err := radiusFiles(source)
	if err != nil {
		return nil, err
	}
	for _, file := range radius {
		candidates[file] = true
	}
	for _, file := range sortedKeys(candidates) {
		if seen[file] {
			continue
		}
		reason, err := excludedReason(file)
		if err != nil {
			reason = "unsupported or unsafe export path"
		}
		if reason != "" {
			p.Excluded = append(p.Excluded, Excluded{file, reason})
		} else {
			p.Unselected = append(p.Unselected, file)
		}
	}
	b, err := jsonBytes(p.Review)
	if err != nil {
		return nil, err
	}
	p.Approval = hash(b)
	return p, nil
}
func regexpName(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
			b.WriteRune(c)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func withNewDirectory(destination string, operation func(string) error) (string, error) {
	abs, err := filepath.Abs(destination)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		return "", err
	}
	if err := os.Mkdir(abs, 0755); err != nil {
		if os.IsExist(err) {
			return "", fmt.Errorf("destination already exists: %s; choose a new directory", abs)
		}
		return "", err
	}
	if err := operation(abs); err != nil {
		if cleanup := os.RemoveAll(abs); cleanup != nil {
			return "", errors.Join(err, fmt.Errorf("cleanup failed; inspect %s: %w", abs, cleanup))
		}
		return "", err
	}
	return abs, nil
}
func writeExclusive(root, file string, b []byte) error {
	if err := safePath(file); err != nil {
		return err
	}
	target := filepath.Join(root, filepath.FromSlash(file))
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(b)
	closeErr := f.Close()
	return errors.Join(writeErr, closeErr)
}
func WritePack(p *Plan, destination, approval string) (string, error) {
	if len(p.Blockers) > 0 {
		return "", fmt.Errorf("export is blocked; resolve the policy findings first")
	}
	b, err := jsonBytes(p.Review)
	if err != nil {
		return "", err
	}
	if approval != hash(b) {
		return "", fmt.Errorf("approval does not match the current inventory; review create --dry-run again")
	}
	if err := validateManifest(p.Manifest); err != nil {
		return "", err
	}
	for _, file := range p.Manifest.Files {
		if file.Operation != "add" && file.Operation != "replace" {
			continue
		}
		payload, ok := p.Payloads[file.Path]
		if !ok || hash(payload) != file.After.SHA256 {
			return "", fmt.Errorf("payload changed after review")
		}
		if err := assertPortableFile(payload, file.Path); err != nil {
			return "", err
		}
	}
	return withNewDirectory(destination, func(output string) error {
		for _, file := range sortedKeys(p.Payloads) {
			if err := writeExclusive(output, "overlay/"+file, p.Payloads[file]); err != nil {
				return err
			}
		}
		b, err := jsonBytes(p.Manifest)
		if err != nil {
			return err
		}
		if err := writeExclusive(output, "radpack.json", b); err != nil {
			return err
		}
		_, err = LoadPack(output)
		return err
	})
}
func LoadPack(directory string) (*Pack, error) {
	root, err := canonicalDirectory(directory)
	if err != nil {
		return nil, err
	}
	file, err := readRegular(root, "radpack.json")
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, fmt.Errorf("pack has no radpack.json")
	}
	var m Manifest
	if err := decodeStrict(file.Bytes, &m); err != nil {
		return nil, err
	}
	if err := validateManifest(m); err != nil {
		return nil, err
	}
	p := &Pack{Manifest: m, Payloads: map[string][]byte{}, Digest: hash(file.Bytes), Raw: file.Bytes}
	expected := map[string]bool{"radpack.json": true}
	total := 0
	for _, entry := range m.Files {
		if entry.Operation != "add" && entry.Operation != "replace" {
			continue
		}
		file := "overlay/" + entry.Path
		expected[file] = true
		payload, err := readRegular(root, file)
		if err != nil {
			return nil, err
		}
		if payload == nil || hash(payload.Bytes) != entry.After.SHA256 {
			return nil, fmt.Errorf("missing or modified payload: %s", entry.Path)
		}
		if err := assertPortableFile(payload.Bytes, entry.Path); err != nil {
			return nil, err
		}
		total += len(payload.Bytes)
		if total > maxPackBytes {
			return nil, fmt.Errorf("payload exceeds 32 MiB limit")
		}
		p.Payloads[entry.Path] = payload.Bytes
	}
	found := 0
	err = filepath.WalkDir(root, func(absolute string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if absolute == root {
			return nil
		}
		relative, err := filepath.Rel(root, absolute)
		if err != nil {
			return err
		}
		file := filepath.ToSlash(relative)
		if err := safePath(file); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("pack contains a symbolic link: %s", file)
		}
		if entry.IsDir() {
			for item := range expected {
				if strings.HasPrefix(item, file+"/") {
					return nil
				}
			}
			return fmt.Errorf("undeclared directory in pack: %s", file)
		}
		if !entry.Type().IsRegular() || !expected[file] {
			return fmt.Errorf("undeclared or unsupported file in pack: %s", file)
		}
		found++
		return nil
	})
	if err != nil {
		return nil, err
	}
	if found != len(expected) {
		return nil, fmt.Errorf("pack payload is incomplete")
	}
	return p, nil
}
func verifyDependencies(destination string, m Manifest) error {
	declared := map[string]bool{}
	for _, file := range m.Files {
		if file.After != nil {
			declared[file.Path] = true
		}
	}
	total := 0
	for _, file := range m.Files {
		if file.After == nil {
			continue
		}
		current, err := readRegular(destination, file.Path)
		if err != nil {
			return err
		}
		if current == nil {
			return fmt.Errorf("declared file is missing: %s", file.Path)
		}
		total += len(current.Bytes)
		if total > maxPackBytes {
			return fmt.Errorf("declared files exceed 32 MiB limit")
		}
		if err := assertPortableFile(current.Bytes, file.Path); err != nil {
			return err
		}
		required, err := dependencies(current.Bytes, file.Path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(file.Path, ".bicep") {
			config, err := nearestConfig(destination, file.Path)
			if err != nil {
				return err
			}
			if config != "" {
				required = append(required, config)
			}
		}
		for _, dep := range required {
			if !declared[dep] {
				return fmt.Errorf("undeclared local dependency of %s: %s", file.Path, dep)
			}
			f, err := readRegular(destination, dep)
			if err != nil {
				return err
			}
			if f == nil {
				return fmt.Errorf("missing local dependency: %s", dep)
			}
		}
	}
	return nil
}
