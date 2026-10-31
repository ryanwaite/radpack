package radpack

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv("RADPACK_VERSION_TEST_HELPER"); mode != "" {
		if strings.Join(os.Args[1:], " ") != "version --cli --output json" {
			os.Exit(2)
		}
		switch mode {
		case "release":
			fmt.Print(`{"release":"v0.60.0","version":"build-sha","commit":"abc"}`)
		case "edge":
			fmt.Print(`{"release":"edge","version":"0.60.0"}`)
		case "invalid":
			fmt.Print("secret-content-not-to-echo")
		case "large":
			fmt.Print(strings.Repeat("x", 65*1024))
		default:
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func stringPointer(value string) *string { return &value }

func TestRadiusVersionDetection(t *testing.T) {
	for _, tc := range []struct {
		name, pin, originVersion, originHash, override, want, source, warning string
	}{
		{"pin", "br:ghcr.io/radius-project/bicep-extension/radius:0.60.0", "", "", "", "0.60.0", "bicepconfig.json", ""},
		{"alias-v-prefix", "br/public:radius:v0.60.0-rc1", "", "", "", "0.60.0-rc1", "bicepconfig.json", ""},
		{"matching", "br:registry/radius:0.60", "0.60.0", "match", "", "0.60", "bicepconfig.json", ""},
		{"mismatch", "br:registry/radius:0.61.0", "0.60.0", "match", "", "0.61.0", "bicepconfig.json", "differs from origin-record"},
		{"stale", "", "0.60.0", "stale", "", "", "unknown", "does not match"},
		{"origin-only", "", "0.60.0", "match", "", "0.60.0", "app.origin.json", ""},
		{"latest", "br:registry/radius:latest", "", "", "", "", "unknown", "does not identify"},
		{"latest-origin", "br:registry/radius:latest", "0.60.0", "match", "", "0.60.0", "app.origin.json", "mutable tags"},
		{"digest", "br:registry/radius@sha256:" + strings.Repeat("a", 64), "", "", "", "", "unknown", "does not identify"},
		{"local", "./radius.tgz", "", "", "", "", "unknown", "does not identify"},
		{"override", "br:registry/radius:0.60.0", "", "", "0.61.0", "0.61.0", "author-supplied", "overrides detected"},
		{"override-no-evidence", "", "", "", "0.61.0", "0.61.0", "author-supplied", ""},
		{"no-evidence", "", "", "", "", "", "unknown", "could not be determined"},
		{"origin-edge", "", "edge", "match", "", "", "unknown", "does not identify"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, ".radius/app.bicep", app)
			if tc.pin != "" {
				writeJSON(t, root, ".radius/bicepconfig.json", map[string]any{"extensions": map[string]string{"radius": tc.pin}})
			}
			if tc.originVersion != "" {
				digest := "sha256:" + hash([]byte(app))
				if tc.originHash == "stale" {
					digest = "sha256:" + strings.Repeat("0", 64)
				}
				writeJSON(t, root, ".radius/app.origin.json", map[string]string{"radiusVersion": tc.originVersion, "appBicepHash": digest})
			}
			var override *string
			if tc.override != "" {
				override = &tc.override
			}
			version, evidence, err := detectRadiusVersion(root, ".radius/app.bicep", override)
			check(t, err)
			got := ""
			if version != nil {
				got = *version
			}
			if got != tc.want || !strings.Contains(evidence.Source, tc.source) {
				t.Fatalf("version=%q evidence=%+v", got, evidence)
			}
			if tc.warning != "" && !strings.Contains(strings.Join(evidence.Warnings, " "), tc.warning) {
				t.Fatal(evidence)
			}
			if tc.warning == "" && len(evidence.Warnings) > 0 {
				t.Fatal(evidence)
			}
		})
	}
}

func TestRadiusVersionNearestConfigAndOriginErrors(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".radius/app.bicep", app)
	write(t, root, "bicepconfig.json", `{"extensions":{"radius":"br:registry/radius:0.60.0"}}`)
	v, _, err := detectRadiusVersion(root, ".radius/app.bicep", nil)
	check(t, err)
	if v == nil || *v != "0.60.0" {
		t.Fatal(v)
	}
	write(t, root, ".radius/bicepconfig.json", `{"extensions":{"radius":"br:registry/radius:0.61.0"}}`)
	write(t, root, ".radius/app.origin.json", `broken: secret-not-for-output`)
	v, evidence, err := detectRadiusVersion(root, ".radius/app.bicep", nil)
	check(t, err)
	if v == nil || *v != "0.61.0" || evidence.Validation != "unusable origin record" {
		t.Fatal(v, evidence)
	}
	if strings.Contains(strings.Join(evidence.Warnings, " "), "secret-not-for-output") {
		t.Fatal("origin leaked")
	}
	_, _, err = detectRadiusVersion(root, ".radius/app.bicep", stringPointer("latest"))
	fails(t, err, "exact release")
	write(t, root, ".radius/bicepconfig.json", `invalid secret-not-for-output`)
	_, _, err = detectRadiusVersion(root, ".radius/app.bicep", nil)
	fails(t, err, "invalid compiler configuration")
}

func TestCreateDetectsRadiusVersionAndBindsReview(t *testing.T) {
	f := newFixture(t, false)
	plan := f.plan(t)
	if plan.Manifest.RadiusVersion == nil || *plan.Manifest.RadiusVersion != "0.60.0" {
		t.Fatal(plan.Manifest)
	}
	if plan.RadiusEvidence.Source != ".radius/bicepconfig.json (extensions.radius)" {
		t.Fatal(plan.RadiusEvidence)
	}
	writeJSON(t, f.source, ".radius/app.origin.json", map[string]string{
		"radiusVersion": "0.61.0", "appBicepHash": "sha256:" + hash([]byte(app)),
	})
	newPlan := f.plan(t)
	if plan.Approval == newPlan.Approval || len(newPlan.RadiusEvidence.Warnings) == 0 {
		t.Fatal("evidence not reflected in review")
	}
	newPlan.RadiusEvidence.Warnings = append(newPlan.RadiusEvidence.Warnings, "changed after review")
	_, err := WritePack(newPlan, f.pack, newPlan.Approval)
	fails(t, err, "approval does not match")
}

func TestInstallRadiusMismatchIsWarning(t *testing.T) {
	for _, latest := range []bool{false, true} {
		t.Run(fmt.Sprint(latest), func(t *testing.T) {
			f := newFixture(t, false)
			f.publish(t)
			var messages []string
			calls := 0
			result, err := InstallApplication(ctx, f.pack, ApplicationOptions{
				Directory: f.destination, Latest: latest, Adapt: latest,
				VersionProbe: func(context.Context) (string, error) { calls++; return "0.61.0", nil },
			}, func(s string) { messages = append(messages, s) })
			check(t, err)
			if calls != 1 || !strings.Contains(strings.Join(messages, "\n"), "Pack records Radius 0.60.0, but the local CLI reports 0.61.0") {
				t.Fatal(messages)
			}
			want := "installed"
			if latest {
				want = "adaptation-required"
			}
			if result.Status != want {
				t.Fatal(result)
			}
			_, err = VerifyInstallation(ctx, "", f.destination)
			check(t, err)
		})
	}
}

func TestRadiusVersionComparison(t *testing.T) {
	for _, tc := range []struct {
		expected *string
		actual   string
		err      error
		warnings int
	}{
		{stringPointer("0.60"), "0.60.0", nil, 0},
		{stringPointer("0.60.0-rc1"), "0.60.0", nil, 1},
		{stringPointer("0.61.0"), "0.60.0", nil, 1},
		{stringPointer("0.60.0"), "", errors.New("secret-not-for-output"), 1},
		{nil, "", nil, 1},
	} {
		warnings := radiusInstallWarnings(ctx, tc.expected, func(context.Context) (string, error) { return tc.actual, tc.err })
		if len(warnings) != tc.warnings || strings.Contains(strings.Join(warnings, " "), "secret-not-for-output") {
			t.Fatal(warnings)
		}
	}
}

func TestLocalRadiusVersionProbe(t *testing.T) {
	exe, err := os.Executable()
	check(t, err)
	t.Setenv("RADIUS_RAD_BINARY", exe)
	for _, mode := range []string{"release", "edge", "invalid", "large", "failure"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("RADPACK_VERSION_TEST_HELPER", mode)
			version, err := localRadiusVersion(ctx)
			if mode == "release" {
				check(t, err)
				if version != "0.60.0" {
					t.Fatal(version)
				}
			} else if err == nil {
				t.Fatal("invalid version accepted")
			}
			if err != nil && strings.Contains(err.Error(), "secret-content") {
				t.Fatal("raw output leaked")
			}
		})
	}
	t.Setenv("RADIUS_RAD_BINARY", filepath.Join(t.TempDir(), "missing"))
	if _, err := localRadiusVersion(ctx); err == nil {
		t.Fatal("missing binary accepted")
	}
	t.Setenv("RADIUS_RAD_BINARY", "")
	t.Setenv("PATH", t.TempDir())
	if _, err := localRadiusVersion(ctx); err == nil {
		t.Fatal("missing rad accepted")
	}
}

func TestCreatePreviewVersionSource(t *testing.T) {
	f := newFixture(t, false)
	var out strings.Builder
	err := run(ctx, []string{"create", "--source", f.source, "--base", f.base, "--dry-run"}, &out,
		func(context.Context) (string, error) { t.Fatal("create should not execute Radius"); return "", nil })
	check(t, err)
	if !strings.Contains(out.String(), "Radius release annotation: 0.60.0") ||
		!strings.Contains(out.String(), "Radius version source: .radius/bicepconfig.json") {
		t.Fatal(out.String())
	}
}

func TestInRepositoryVersionInferenceAndOverrideWarnings(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			f := newFixture(t, false)
			d := descriptor()
			if explicit {
				d.RadiusVersion = stringPointer("0.61.0")
			}
			writeJSON(t, f.source, ".radpack/radpack.json", d)
			commit(t, f.source, ".radius", ".radpack")
			var output strings.Builder
			_, err := InstallApplication(ctx, "radpack-fixture/app", ApplicationOptions{
				Directory:    f.destination,
				VersionProbe: func(context.Context) (string, error) { return "0.60.0", nil },
			}, func(s string) { output.WriteString(s + "\n") })
			check(t, err)
			p, err := LoadPack(filepath.Join(f.destination, ".radpack/pack"))
			check(t, err)
			want := "0.60.0"
			if explicit {
				want = "0.61.0"
			}
			if p.Manifest.RadiusVersion == nil || *p.Manifest.RadiusVersion != want {
				t.Fatal(p.Manifest)
			}
			if explicit && !strings.Contains(output.String(), "overrides detected") {
				t.Fatal(output.String())
			}
		})
	}
}

func TestVersionWarningsNeverFailCLIInstallation(t *testing.T) {
	for _, mode := range []string{"mismatch", "unavailable", "unrecorded"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, false)
			if mode == "unrecorded" {
				write(t, f.source, ".radius/bicepconfig.json", `{"extensions":{"radius":"br:registry/radius:latest"}}`)
			}
			f.publish(t)
			var out strings.Builder
			err := run(ctx, []string{"install", f.pack, "--directory", f.destination}, &out,
				func(context.Context) (string, error) {
					if mode == "unavailable" {
						return "", errors.New("private output")
					}
					if mode == "unrecorded" {
						t.Fatal("unrecorded pack should not probe CLI")
					}
					return "0.61.0", nil
				})
			check(t, err)
			if !strings.Contains(out.String(), "Warning:") || !strings.Contains(out.String(), "Installed and verified") ||
				strings.Contains(out.String(), "private output") {
				t.Fatal(out.String())
			}
			_, err = VerifyInstallation(ctx, "", f.destination)
			check(t, err)
		})
	}
}

func TestModelOriginFallbackAndRootLayout(t *testing.T) {
	f := newFixture(t, false)
	check(t, os.RemoveAll(filepath.Join(f.source, ".radius")))
	write(t, f.source, "app.bicep", app)
	writeJSON(t, f.source, "app.origin.json", map[string]string{
		"radiusVersion": "v0.60.0", "appBicepHash": "sha256:" + hash([]byte(app)),
	})
	p := f.publish(t)
	if p.Manifest.RadiusVersion == nil || *p.Manifest.RadiusVersion != "0.60.0" ||
		p.RadiusEvidence.Source != "app.origin.json (matching model hash)" {
		t.Fatal(p)
	}
	f.install(t, false)
}

func TestCanceledVersionProbeIsWarning(t *testing.T) {
	exe, err := os.Executable()
	check(t, err)
	t.Setenv("RADIUS_RAD_BINARY", exe)
	t.Setenv("RADPACK_VERSION_TEST_HELPER", "release")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	warnings := radiusInstallWarnings(cancelled, stringPointer("0.60.0"), localRadiusVersion)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "Installation continues") {
		t.Fatal(warnings)
	}
}
