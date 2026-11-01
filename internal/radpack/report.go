package radpack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

type PackReport struct {
	ReportVersion      int            `json:"reportVersion"`
	Command            string         `json:"command"`
	PackManifestSHA256 string         `json:"packManifestSha256"`
	Application        Upstream       `json:"application"`
	FormatVersion      int            `json:"formatVersion"`
	FileVerification   string         `json:"fileVerification"`
	InstallationStatus string         `json:"installationStatus"`
	Readiness          BuildReadiness `json:"readiness"`
}

// Reports never publish, fetch registries, inspect credentials, compile Bicep,
// or register recipes. Missing retained-file contents are unknown, not success.
func ReportPack(ctx context.Context, packDirectory, installedDirectory string) (PackReport, error) {
	verification := "pack-integrity-checked"
	installationStatus := "not-installed"
	selected := ""
	if installedDirectory != "" {
		v, err := VerifyInstallation(ctx, "", installedDirectory)
		if err != nil {
			return PackReport{}, err
		}
		selected = v.Upstream
		verification = "installed-snapshot-checked"
		installationStatus = v.Status
		packDirectory = filepath.Join(installedDirectory, ".radpack", "pack")
	}
	pack, err := LoadPack(packDirectory)
	if err != nil {
		return PackReport{}, err
	}
	assets := map[string][]byte{}
	missing := []string{}
	absent := []string{}
	for _, file := range pack.Manifest.Files {
		if file.After == nil {
			continue
		}
		if installedDirectory != "" {
			current, err := readRegular(installedDirectory, file.Path)
			if err != nil {
				return PackReport{}, err
			}
			if current != nil {
				assets[file.Path] = current.Bytes
			} else {
				absent = append(absent, file.Path)
			}
		} else if payload, ok := pack.Payloads[file.Path]; ok {
			assets[file.Path] = payload
		} else {
			missing = append(missing, file.Path)
		}
	}
	m := pack.Manifest
	if selected != "" {
		m.Upstream.Commit = selected
		tree, err := treeAt(ctx, installedDirectory, selected)
		if err != nil {
			return PackReport{}, err
		}
		m.Files = append([]FileOperation{}, m.Files...)
		for index, entry := range m.Files {
			before, err := baseFile(ctx, installedDirectory, tree, entry.Path)
			if err != nil {
				return PackReport{}, err
			}
			after, err := readRegular(installedDirectory, entry.Path)
			if err != nil {
				return PackReport{}, err
			}
			op := "replace"
			switch {
			case same(fingerprint(before), fingerprint(after)):
				op = "retain"
			case before == nil:
				op = "add"
			case after == nil:
				op = "delete"
			}
			m.Files[index] = FileOperation{entry.Path, op, fingerprint(before), fingerprint(after)}
		}
	}
	readiness := assessBuildReadiness(m, assets)
	if len(missing) > 0 {
		for _, file := range missing {
			readiness.Issues = append(readiness.Issues, ReadinessIssue{"retained-content-not-inspected", "source", file,
				"Retained upstream contents are not stored in the pack directory.", "Run report --directory against a verified installed checkout to inspect these contents."})
		}
		if readiness.Status != "requires-source-assembly" {
			readiness.Status = "review-required"
		}
	}
	for _, file := range absent {
		readiness.Issues = append(readiness.Issues, ReadinessIssue{"required-asset-absent", "model", file,
			"A reference pack asset is absent from this installed adaptation snapshot.", "Resolve the missing resource or recipe with the Radius tools before deployment."})
		if readiness.Status != "requires-source-assembly" {
			readiness.Status = "review-required"
		}
	}
	return PackReport{1, "report", pack.Digest, m.Upstream, m.FormatVersion, verification, installationStatus, readiness}, nil
}

type CreateReport struct {
	ReportVersion int              `json:"reportVersion"`
	Command       string           `json:"command"`
	Status        string           `json:"status"`
	Stage         string           `json:"stage"`
	Output        string           `json:"output,omitempty"`
	ReviewSHA256  string           `json:"reviewSha256,omitempty"`
	Review        *Review          `json:"review,omitempty"`
	Diagnostics   []ReadinessIssue `json:"diagnostics"`
}

func createJSONCommand(ctx context.Context, values map[string][]string, out io.Writer) (resultErr error) {
	get := func(key string) string {
		if len(values[key]) > 0 {
			return values[key][0]
		}
		return ""
	}
	result := CreateReport{ReportVersion: 1, Command: "create", Status: "blocked", Stage: "source", Diagnostics: []ReadinessIssue{}}
	defer func() {
		if resultErr != nil {
			if result.Status == "saved" {
				result.Status = "saved-with-cleanup-error"
			} else {
				result.Status = "blocked"
			}
			code := "create-" + result.Stage + "-failed"
			next := "Resolve the reported issue and rerun create --json. No publication or deployment has occurred."
			if result.Status == "saved-with-cleanup-error" {
				next = "The pack was saved. Inspect the cleanup error; do not overwrite or recreate the existing output."
			}
			if result.Stage == "source" {
				next = "Confirm repository access and specify --upstream or --base if source selection is ambiguous. Credentials were not inspected."
				switch {
				case strings.Contains(resultErr.Error(), "multiple source repositories"), strings.Contains(resultErr.Error(), "no source remote"):
					code = "source-repository-required"
				case strings.Contains(resultErr.Error(), "not confirmed published"):
					code = "source-revision-required"
				default:
					code = "source-access-unverified"
				}
			}
			if strings.Contains(resultErr.Error(), "no application model found") {
				code = "model-required"
				next = "Prepare the application's Radius model, then resume create from that checkout."
			}
			result.Diagnostics = append(result.Diagnostics, ReadinessIssue{code, result.Stage, "", resultErr.Error(), next})
		}
		data, err := jsonBytes(result)
		if err == nil {
			_, err = out.Write(data)
		}
		resultErr = errors.Join(resultErr, err)
	}()
	if get("approve") != "" && get("dry-run") != "" {
		return fmt.Errorf("use --dry-run or --approve, not both")
	}
	var version *string
	if _, ok := values["radius-version"]; ok {
		v := get("radius-version")
		version = &v
	}
	source, err := prepareCreateSource(ctx, CreateOptions{
		Source: get("source"), Base: get("base"), Upstream: get("upstream"), Name: get("name"), RadiusVersion: version,
		Include: values["include"], Recipes: values["recipe"], RecipePacks: values["recipe-pack"],
	}, nil)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, source.close()) }()
	result.Stage = "model"
	p, err := PlanCreate(ctx, source.Options)
	if err != nil {
		return err
	}
	result.Review = &p.Review
	output := get("output")
	if output == "" {
		output = filepath.Join("contrib", p.Manifest.Name)
	}
	result.Output, err = filepath.Abs(output)
	if err != nil {
		return err
	}
	result.Stage = "review"
	if len(p.Blockers) > 0 {
		result.Stage = "policy"
		return fmt.Errorf("export blocked: %s", strings.Join(p.Blockers, "; "))
	}
	result.ReviewSHA256 = p.Approval
	if get("approve") == "" {
		result.Status = "awaiting-review"
		return nil
	}
	result.Stage = "save"
	fresh, err := PlanCreate(ctx, source.Options)
	if err != nil {
		return err
	}
	if len(fresh.Blockers) > 0 || fresh.Approval != p.Approval {
		return fmt.Errorf("checkout changed during review; no pack written")
	}
	if _, err := WritePack(fresh, result.Output, get("approve")); err != nil {
		return err
	}
	result.Status = "saved"
	return nil
}
