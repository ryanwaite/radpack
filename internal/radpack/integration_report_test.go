package radpack

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const reusablePack = `extension radius
param registry string
resource recipes 'Radius.Core/recipePacks@2025-08-01-preview' = {
  name: 'community-recipes'
  properties: {
    recipes: {
      'Radius.Compute/containerImages': {
        kind: 'bicep'
        source: 'ghcr.io/example/recipes/containerimages@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
        parameters: { registry: registry }
      }
    }
  }
}
`
const recipe = `param context object
output result object = { values: {} }
`

func recipePlan(t *testing.T, f *fixture) *Plan {
	t.Helper()
	write(t, f.source, ".radius/radius-recipe-pack.bicep", reusablePack)
	write(t, f.source, ".radius/recipes/image.bicep", recipe)
	p, err := PlanCreate(ctx, CreateOptions{
		Source: f.source, Base: f.base,
		Recipes:     []string{".radius/recipes/image.bicep"},
		RecipePacks: []string{".radius/radius-recipe-pack.bicep"},
	})
	check(t, err)
	if len(p.Blockers) > 0 {
		t.Fatal(p.Blockers)
	}
	return p
}

func TestExplicitRecipesRoundTrip(t *testing.T) {
	f := newFixture(t, false)
	p := recipePlan(t, f)
	if p.Manifest.FormatVersion != 2 || p.Manifest.Integration == nil {
		t.Fatal(p.Manifest)
	}
	_, err := WritePack(p, f.pack, p.Approval)
	check(t, err)
	loaded, err := LoadPack(f.pack)
	check(t, err)
	if loaded.Manifest.Integration.RecipePacks[0] != ".radius/radius-recipe-pack.bicep" {
		t.Fatal(loaded.Manifest)
	}
	f.install(t, false)
	if read(t, f.destination, ".radius/radius-recipe-pack.bicep") != reusablePack {
		t.Fatal("recipe pack lost")
	}
	_, err = VerifyInstallation(ctx, "", f.destination)
	check(t, err)
	result, err := ReportPack(ctx, "", f.destination)
	check(t, err)
	if len(result.Readiness.Recipes) != 1 || len(result.Readiness.RecipePacks) != 1 ||
		result.Readiness.RecipeRegistration != "not-checked" || result.Readiness.Credentials != "not-inspected" ||
		result.Readiness.DeploymentVerification != "not-performed" {
		t.Fatal(result)
	}
}

func TestRecipeRoleCannotBypassEnvironmentPolicy(t *testing.T) {
	for _, mode := range []string{"environment", "credential", "not-a-pack", "wrong-extension", "forbidden-parent"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, false)
			content := reusablePack
			file := ".radius/radius-recipe-pack.bicep"
			switch mode {
			case "environment":
				content += "\nresource env 'Radius.Core/environments@2025-08-01-preview' = {}\n"
			case "credential":
				content += "\nvar credentials = { password: 'do-not-export' }\n"
			case "not-a-pack":
				content = "var name = 'not a recipe pack'\n"
			case "wrong-extension":
				file = ".radius/packs.json"
			case "forbidden-parent":
				file = ".aws/radius-recipe-pack.bicep"
			}
			write(t, f.source, file, content)
			p, err := PlanCreate(ctx, CreateOptions{Source: f.source, Base: f.base, RecipePacks: []string{file}})
			if err == nil && len(p.Blockers) == 0 {
				t.Fatal("invalid recipe pack accepted")
			}
		})
	}
}

func TestGeneratedNameNeedsExplicitRecipeRole(t *testing.T) {
	f := newFixture(t, false)
	write(t, f.source, ".radius/radius-recipe-pack.bicep", reusablePack)
	plain := f.plan(t, ".radius/radius-recipe-pack.bicep")
	if len(plain.Blockers) == 0 {
		t.Fatal("unclassified generated pack included")
	}
	p := recipePlan(t, f)
	_, err := WritePack(p, f.pack, p.Approval)
	check(t, err)
	raw := read(t, f.pack, "radpack.json")
	var m Manifest
	check(t, json.Unmarshal([]byte(raw), &m))
	m.Integration = nil
	m.FormatVersion = 1
	writeJSON(t, f.pack, "radpack.json", m)
	_, err = LoadPack(f.pack)
	fails(t, err, "forbidden pack path")
}

func TestRecipeRoleValidation(t *testing.T) {
	f := newFixture(t, false)
	p := recipePlan(t, f)
	raw, err := jsonBytes(p.Manifest)
	check(t, err)
	for _, mutate := range []func(*Manifest){
		func(m *Manifest) { m.FormatVersion = 1 },
		func(m *Manifest) { m.Integration = nil },
		func(m *Manifest) { m.Integration.Recipes = append(m.Integration.Recipes, "missing.bicep") },
		func(m *Manifest) { m.Integration.Recipes = append(m.Integration.Recipes, m.Integration.RecipePacks[0]) },
		func(m *Manifest) { m.Integration.RecipePacks[0] = "../outside" },
		func(m *Manifest) { m.Integration.Recipes = nil },
		func(m *Manifest) { m.Integration.Recipes = append(m.Integration.Recipes, m.Application) },
	} {
		var m Manifest
		check(t, json.Unmarshal(raw, &m))
		mutate(&m)
		if validateManifest(m) == nil {
			t.Fatal("invalid roles accepted")
		}
	}
	p.Manifest.Integration.Recipes = append(p.Manifest.Integration.Recipes, "changed.bicep")
	_, err = WritePack(p, f.pack, p.Approval)
	fails(t, err, "approval does not match")
}

func TestStandardCustomPackAndRetainedDefinitions(t *testing.T) {
	f := newFixture(t, false)
	write(t, f.source, ".radius/custom-recipe-pack.bicep", reusablePack)
	p := f.plan(t)
	if p.Manifest.FormatVersion != 2 || len(p.Manifest.Integration.RecipePacks) != 1 {
		t.Fatal(p.Manifest)
	}
	base := commit(t, f.source, ".radius")
	f.base = base
	p = f.plan(t)
	_, err := WritePack(p, f.pack, p.Approval)
	check(t, err)
	if len(p.Payloads) != 0 {
		t.Fatal("retained recipe assets duplicated")
	}
	result, err := ReportPack(ctx, f.pack, "")
	check(t, err)
	found := false
	for _, issue := range result.Readiness.Issues {
		if issue.Code == "retained-content-not-inspected" {
			found = true
		}
	}
	if !found || result.Readiness.Status == "reference-checks-passed" {
		t.Fatal(result)
	}
	f.install(t, false)
	result, err = ReportPack(ctx, "", f.destination)
	check(t, err)
	for _, issue := range result.Readiness.Issues {
		if issue.Code == "retained-content-not-inspected" {
			t.Fatal(result)
		}
	}
}

func TestInRepositoryRecipeDescriptor(t *testing.T) {
	f := newFixture(t, false)
	p := recipePlan(t, f)
	d := descriptor()
	d.FormatVersion = 2
	d.Integration = p.Manifest.Integration
	writeJSON(t, f.source, ".radpack/radpack.json", d)
	commit(t, f.source, ".radius", ".radpack")
	result, err := InstallApplication(ctx, "radpack-fixture/app", ApplicationOptions{Directory: f.destination}, nil)
	check(t, err)
	if result.PackSource.Kind != "in-repository" {
		t.Fatal(result)
	}
	pack, err := LoadPack(filepath.Join(f.destination, ".radpack/pack"))
	check(t, err)
	if pack.Manifest.FormatVersion != 2 || pack.Manifest.Integration == nil {
		t.Fatal(pack)
	}
}

func TestRecipeDependencyCaptureAndTerraformReview(t *testing.T) {
	f := newFixture(t, false)
	write(t, f.source, ".radius/custom-recipe-pack.bicep", reusablePack+"\nmodule shared './recipes/shared.bicep' = { name: 'shared' }\n")
	write(t, f.source, ".radius/recipes/shared.bicep", recipe)
	write(t, f.source, ".radius/recipes/main.tf", `variable "context" { type = any }`)
	p, err := PlanCreate(ctx, CreateOptions{Source: f.source, Base: f.base, Recipes: []string{".radius/recipes/main.tf"}})
	check(t, err)
	if len(p.Blockers) > 0 {
		t.Fatal(p.Blockers)
	}
	hasDependency := false
	for _, file := range p.Manifest.Files {
		if file.Path == ".radius/recipes/shared.bicep" {
			hasDependency = true
		}
	}
	if !hasDependency {
		t.Fatal("Bicep module missing")
	}
	hasTerraform := false
	for _, issue := range p.Readiness.Issues {
		if issue.Code == "terraform-module-review-required" {
			hasTerraform = true
		}
	}
	if !hasTerraform {
		t.Fatal(p.Readiness)
	}
}

func TestBuildReadinessStates(t *testing.T) {
	sha := strings.Repeat("a", 40)
	model := ".radius/app.bicep"
	m := Manifest{FormatVersion: 1, Name: "app", Upstream: Upstream{repo, sha}, Application: model, Files: []FileOperation{}}
	for _, tc := range []struct {
		name, source, status, code string
		changed                    bool
	}{
		{"matching", "git::" + repo + "//src?ref=" + sha, "reference-checks-passed", "", false},
		{"overlay", "git::" + repo + "//src?ref=" + sha, "requires-source-assembly", "overlay-not-in-git-build", true},
		{"revision", "git::" + repo + "//src?ref=" + strings.Repeat("b", 40), "review-required", "build-revision-mismatch", false},
		{"mutable", "git::" + repo + "//src?ref=main", "review-required", "unresolved-build-revision", false},
		{"external", "git::https://github.com/sally/app.git//src?ref=" + sha, "review-required", "external-build-repository", false},
		{"dynamic", "git::" + repo + "//src?ref=${revision}", "review-required", "dynamic-build-source", false},
		{"none", "", "not-assessed", "no-literal-git-build-sources", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := m
			if tc.changed {
				manifest.Files = []FileOperation{{Path: "src/nginx.conf", Operation: "replace"}}
			}
			report := assessBuildReadiness(manifest, map[string][]byte{model: []byte("var source = '" + tc.source + "'")})
			if report.Status != tc.status {
				t.Fatal(report)
			}
			if tc.code != "" {
				found := false
				for _, issue := range report.Issues {
					if issue.Code == tc.code && issue.NextAction != "" {
						found = true
					}
				}
				if !found {
					t.Fatal(report)
				}
			}
			if report.Compilation != "not-performed" || report.Credentials != "not-inspected" {
				t.Fatal(report)
			}
		})
	}
}

func TestRecipeReferencesAreNotApplicationBuildSources(t *testing.T) {
	file := ".radius/custom-recipe-pack.bicep"
	m := Manifest{Integration: &IntegrationAssets{Recipes: []string{}, RecipePacks: []string{file}}}
	for _, tc := range []struct {
		source string
		pinned bool
	}{
		{"git::https://github.com/example/recipes.git//recipe?ref=" + strings.Repeat("a", 40), true},
		{"ghcr.io/example/recipes@sha256:" + strings.Repeat("b", 64), true},
		{"ghcr.io/example/recipes:latest", false},
		{"ghcr.io/example/recipes:1.2.3", false},
		{"git::https://github.com/example/recipes.git//recipe?ref=main", false},
		{"${recipeSource}", false},
	} {
		report := assessBuildReadiness(m, map[string][]byte{file: []byte("source: '" + tc.source + "'")})
		if len(report.BuildSources) != 0 || len(report.RecipeSources) != 1 {
			t.Fatal(report)
		}
		got := report.RecipeSources[0].Status == "immutable-reference"
		if got != tc.pinned {
			t.Fatal(report)
		}
		if report.RecipeRegistration != "not-checked" {
			t.Fatal("claimed registration")
		}
	}
}

func TestCreateJSONReviewSaveAndBlocked(t *testing.T) {
	f := newFixture(t, false)
	recipePlan(t, f)
	args := []string{"create", "--source", f.source, "--output", f.pack, "--recipe", ".radius/recipes/image.bicep",
		"--recipe-pack", ".radius/radius-recipe-pack.bicep", "--json"}
	var out strings.Builder
	ui := &createUI{reader: nil, out: &out}
	check(t, runWithUI(ctx, args, &out, nil, ui))
	var result CreateReport
	check(t, json.Unmarshal([]byte(out.String()), &result))
	if result.Status != "awaiting-review" || result.Review == nil || result.Review.Manifest.FormatVersion != 2 ||
		!hashPattern.MatchString(result.ReviewSHA256) {
		t.Fatal(out.String())
	}
	notCreated(t, f.pack)
	out.Reset()
	check(t, run(ctx, append(args, "--approve", result.ReviewSHA256), &out, nil))
	check(t, json.Unmarshal([]byte(out.String()), &result))
	if result.Status != "saved" {
		t.Fatal(out.String())
	}
	out.Reset()
	err := run(ctx, append(args, "--approve", strings.Repeat("0", 64)), &out, nil)
	fails(t, err, "approval does not match")
	check(t, json.Unmarshal([]byte(out.String()), &result))
	if result.Status != "blocked" || result.Stage != "save" {
		t.Fatal(result)
	}
}

func TestCreateJSONModelAndSourceDiagnostics(t *testing.T) {
	f := newFixture(t, false)
	check(t, os.RemoveAll(filepath.Join(f.source, ".radius")))
	var out strings.Builder
	err := run(ctx, []string{"create", "--source", f.source, "--json"}, &out, nil)
	fails(t, err, "no application model")
	var result CreateReport
	check(t, json.Unmarshal([]byte(out.String()), &result))
	if result.Status != "blocked" || result.Diagnostics[0].Code != "model-required" {
		t.Fatal(result)
	}
	g(t, f.source, "remote", "add", "upstream", "https://github.com/original/app.git")
	out.Reset()
	err = run(ctx, []string{"create", "--source", f.source, "--json"}, &out, nil)
	fails(t, err, "multiple source")
	check(t, json.Unmarshal([]byte(out.String()), &result))
	if result.Diagnostics[0].Code != "source-repository-required" {
		t.Fatal(result)
	}
}

func TestReportCLIAndBuildAssemblyGap(t *testing.T) {
	f := newFixture(t, false)
	write(t, f.source, ".radius/app.bicep", app+"\nvar source = 'git::"+repo+"?ref="+f.base+"'\n")
	write(t, f.source, "app.js", "adaptation")
	f.publish(t, "app.js")
	var out strings.Builder
	check(t, run(ctx, []string{"report", f.pack}, &out, nil))
	var report PackReport
	check(t, json.Unmarshal([]byte(out.String()), &report))
	if report.Readiness.Status != "requires-source-assembly" || report.Readiness.Credentials != "not-inspected" {
		t.Fatal(report)
	}
	f.install(t, false)
	out.Reset()
	check(t, run(ctx, []string{"report", "--directory", f.destination}, &out, nil))
	check(t, json.Unmarshal([]byte(out.String()), &report))
	if report.FileVerification != "installed-snapshot-checked" {
		t.Fatal(report)
	}
}

func TestRecipePackHandoff(t *testing.T) {
	f := newFixture(t, false)
	isolateHandoffHome(t, f)
	p := recipePlan(t, f)
	_, err := WritePack(p, f.pack, p.Approval)
	check(t, err)
	_, err = InstallApplication(ctx, f.pack, ApplicationOptions{Directory: f.destination, Desktop: true}, nil)
	check(t, err)
	worktree := filepath.Join(f.root, "recipe-worktree")
	g(t, f.destination, "worktree", "add", "--quiet", "-b", "recipe", worktree, f.base)
	status, err := HandoffStatusFor(context.Background(), worktree)
	check(t, err)
	_, err = ApplyHandoff(ctx, worktree, status.RequestID, status.ReviewSHA256)
	check(t, err)
	if read(t, worktree, ".radius/radius-recipe-pack.bicep") != reusablePack {
		t.Fatal("pack missing in worktree")
	}
}

func TestExplicitReusablePackAlreadyInSource(t *testing.T) {
	f := newFixture(t, false)
	recipePlan(t, f)
	f.base = commit(t, f.source, ".radius")
	p, err := PlanCreate(ctx, CreateOptions{Source: f.source, Base: f.base, RecipePacks: []string{".radius/radius-recipe-pack.bicep"}})
	check(t, err)
	if len(p.Blockers) > 0 {
		t.Fatal(p.Blockers)
	}
	_, err = WritePack(p, f.pack, p.Approval)
	check(t, err)
	f.install(t, false)
	_, err = VerifyInstallation(ctx, "", f.destination)
	check(t, err)
}

func TestCreateJSONPolicyErrorDoesNotRevealSecret(t *testing.T) {
	f := newFixture(t, false)
	secret := "ghp_" + strings.Repeat("Z", 36)
	write(t, f.source, ".radius/app.bicep", app+"\nvar secretValue = '"+secret+"'")
	var out strings.Builder
	err := run(ctx, []string{"create", "--source", f.source, "--json"}, &out, nil)
	if err == nil {
		t.Fatal("expected blocked export")
	}
	var result CreateReport
	check(t, json.Unmarshal([]byte(out.String()), &result))
	if result.Stage != "policy" || result.Status != "blocked" ||
		strings.Contains(out.String()+err.Error(), secret) {
		t.Fatal("unsafe policy report")
	}
}

func TestReportLatestPreservedSource(t *testing.T) {
	f := newFixture(t, false)
	write(t, f.source, "app.js", "pack adaptation\n")
	write(t, f.source, ".radius/app.bicep", app+"\nvar source = 'git::"+repo+"?ref="+f.base+"'")
	f.publish(t, "app.js")
	write(t, f.source, "app.js", "new upstream\n")
	commit(t, f.source, "app.js")
	_, err := InstallApplication(ctx, f.pack, ApplicationOptions{Directory: f.destination, Latest: true, Adapt: true}, nil)
	check(t, err)
	report, err := ReportPack(ctx, "", f.destination)
	check(t, err)
	if report.InstallationStatus != "adaptation-required" {
		t.Fatal(report)
	}
	for _, issue := range report.Readiness.Issues {
		if issue.Code == "overlay-not-in-git-build" && strings.Contains(issue.Message, "app.js") {
			t.Fatal("preserved latest file incorrectly reported as pack adaptation")
		}
	}
}
