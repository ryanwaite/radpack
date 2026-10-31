package radpack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"
)

// VersionEvidence belongs to the export review, not the portable wire format.
// The manifest continues to use its existing nullable radiusVersion field.
type VersionEvidence struct {
	Source     string   `json:"source"`
	Validation string   `json:"validation"`
	Warnings   []string `json:"warnings"`
}

func releaseVersion(value string) string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if !versionPattern.MatchString(value) {
		return ""
	}
	return value
}

func sameRelease(a, b string) bool {
	normalize := func(value string) string {
		core, suffix, hasSuffix := strings.Cut(releaseVersion(value), "-")
		if strings.Count(core, ".") == 1 {
			core += ".0"
		}
		if hasSuffix {
			return core + "-" + suffix
		}
		return core
	}
	return normalize(a) == normalize(b)
}

// Only an exact version tag on a registry reference identifies a release.
// Digests, local extension packages, and mutable tags are not release evidence.
func extensionRelease(reference string) string {
	if !strings.HasPrefix(reference, "br:") && !strings.HasPrefix(reference, "br/") {
		return ""
	}
	if strings.Contains(reference, "@") {
		return ""
	}
	index := strings.LastIndex(reference, ":")
	if index < 0 {
		return ""
	}
	return releaseVersion(reference[index+1:])
}

func detectRadiusVersion(root, application string, override *string) (*string, VersionEvidence, error) {
	evidence := VersionEvidence{Source: "unknown", Validation: "no origin record", Warnings: []string{}}
	var selected *string
	configPath, err := nearestConfig(root, application)
	if err != nil {
		return nil, evidence, err
	}
	if configPath != "" {
		configFile, err := readRegular(root, configPath)
		if err != nil {
			return nil, evidence, err
		}
		var config struct {
			Extensions map[string]json.RawMessage `json:"extensions"`
		}
		if err := json.Unmarshal(configFile.Bytes, &config); err != nil {
			return nil, evidence, fmt.Errorf("invalid compiler configuration JSON (contents withheld)")
		}
		var reference string
		raw := config.Extensions["radius"]
		if len(raw) > 0 && json.Unmarshal(raw, &reference) == nil {
			if version := extensionRelease(reference); version != "" {
				selected = &version
				evidence.Source = configPath + " (extensions.radius)"
			} else {
				evidence.Warnings = append(evidence.Warnings, "The Radius extension reference does not identify an exact release; mutable tags and local packages are not version evidence.")
			}
		} else {
			evidence.Warnings = append(evidence.Warnings, "The compiler configuration has no usable extensions.radius reference.")
		}
	}

	originPath := path.Join(path.Dir(application), "app.origin.json")
	originFile, err := readRegular(root, originPath)
	if err != nil {
		return nil, evidence, err
	}
	if originFile != nil {
		var origin struct {
			RadiusVersion string `json:"radiusVersion"`
			AppBicepHash  string `json:"appBicepHash"`
		}
		if err := json.Unmarshal(originFile.Bytes, &origin); err != nil {
			evidence.Validation = "unusable origin record"
			evidence.Warnings = append(evidence.Warnings, "The origin record cannot be read; its validation claim was ignored (contents withheld).")
		} else {
			model, err := readRegular(root, application)
			if err != nil {
				return nil, evidence, err
			}
			originVersion := releaseVersion(origin.RadiusVersion)
			if model == nil || origin.AppBicepHash != "sha256:"+hash(model.Bytes) {
				evidence.Validation = "origin model hash does not match"
				evidence.Warnings = append(evidence.Warnings, "The origin record does not match the application model bytes; its version was not used as validation evidence.")
			} else if originVersion == "" {
				evidence.Validation = "origin model hash matches; release unknown"
				evidence.Warnings = append(evidence.Warnings, "The origin record matches the model but does not identify an exact Radius release.")
			} else {
				evidence.Validation = "origin model hash matches; recorded Radius " + originVersion
				if selected == nil {
					selected = &originVersion
					evidence.Source = originPath + " (matching model hash)"
				} else if !sameRelease(*selected, originVersion) {
					evidence.Warnings = append(evidence.Warnings, fmt.Sprintf("Compiler pin Radius %s differs from origin-record Radius %s; the current compiler pin takes precedence. Revalidate through Radius.", *selected, originVersion))
				}
			}
		}
	}
	if override != nil {
		version := releaseVersion(*override)
		if version == "" {
			return nil, evidence, fmt.Errorf("--radius-version must be an exact release version")
		}
		if selected != nil && !sameRelease(*selected, version) {
			evidence.Warnings = append(evidence.Warnings, fmt.Sprintf("Author-supplied Radius %s overrides detected Radius %s. The model and compiler configuration are unchanged.", version, *selected))
		}
		selected = &version
		evidence.Source = "author-supplied --radius-version or in-repository manifest"
	}
	if selected == nil {
		evidence.Warnings = append(evidence.Warnings, "Radius release could not be determined; radiusVersion will remain null. Supply --radius-version only when you know the intended release.")
	}
	return selected, evidence, nil
}

type radiusVersionProbe func(context.Context) (string, error)

// This read-only CLI query never contacts the control plane. It does not install
// Radius, search plugin directories, or change the configured executable.
func localRadiusVersion(ctx context.Context) (string, error) {
	executable := strings.TrimSpace(os.Getenv("RADIUS_RAD_BINARY"))
	if executable == "" {
		var err error
		executable, err = exec.LookPath("rad")
		if err != nil {
			return "", fmt.Errorf("Radius CLI was not found on PATH")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "version", "--cli", "--output", "json")
	cmd.WaitDelay = time.Second
	// A bounded writer without ReadFrom prevents os/exec bypassing the limit.
	output := &versionOutput{}
	cmd.Stdout = output
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("Radius CLI version query failed or timed out; raw output withheld")
	}
	return parseRadiusCLIRelease(output.Bytes())
}

type versionOutput struct{ data bytes.Buffer }

func (b *versionOutput) Write(p []byte) (int, error) {
	if b.data.Len()+len(p) > 64*1024 {
		return 0, fmt.Errorf("version output exceeds limit")
	}
	return b.data.Write(p)
}
func (b *versionOutput) Bytes() []byte { return b.data.Bytes() }

func parseRadiusCLIRelease(data []byte) (string, error) {
	var info struct {
		Release string `json:"release"`
	}
	if json.Unmarshal(data, &info) != nil {
		return "", fmt.Errorf("Radius CLI returned invalid version JSON; contents withheld")
	}
	if version := releaseVersion(info.Release); version != "" {
		return version, nil
	}
	// 'version' may be a build commit. Never interpret it as the release or
	// disguise an edge/dev build as the release it might have been built from.
	return "", fmt.Errorf("Radius CLI did not report an exact release (for example, it may be an edge build)")
}

func radiusInstallWarnings(ctx context.Context, expected *string, probe radiusVersionProbe) []string {
	if expected == nil {
		return []string{"Pack does not record an exact Radius release; local version compatibility was not checked."}
	}
	actual, err := probe(ctx)
	if err != nil {
		// Probe failures never include raw command output or user-supplied paths.
		return []string{"Could not determine the local Radius CLI release. Installation continues; check your Radius tooling before deploying."}
	}
	if !sameRelease(*expected, actual) {
		return []string{fmt.Sprintf("Pack records Radius %s, but the local CLI reports %s. Installation continues; validate the model with the Radius version you will deploy with.", *expected, actual)}
	}
	return nil
}
