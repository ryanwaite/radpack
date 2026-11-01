package radpack

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

type ReadinessIssue struct {
	Code       string `json:"code"`
	Phase      string `json:"phase"`
	File       string `json:"file,omitempty"`
	Message    string `json:"message"`
	NextAction string `json:"nextAction"`
}

type BuildReference struct {
	File       string `json:"file"`
	Repository string `json:"repository,omitempty"`
	Revision   string `json:"revision,omitempty"`
	Context    string `json:"context,omitempty"`
	Status     string `json:"status"`
}

type RecipeReference struct {
	File   string `json:"file"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

type BuildReadiness struct {
	ReportVersion          int               `json:"reportVersion"`
	AnalysisScope          string            `json:"analysisScope"`
	Status                 string            `json:"status"`
	BuildSources           []BuildReference  `json:"buildSources"`
	RecipeSources          []RecipeReference `json:"recipeSources"`
	Recipes                []string          `json:"recipes"`
	RecipePacks            []string          `json:"recipePacks"`
	Issues                 []ReadinessIssue  `json:"issues"`
	Compilation            string            `json:"compilation"`
	RecipeRegistration     string            `json:"recipeRegistration"`
	Credentials            string            `json:"credentials"`
	DeploymentVerification string            `json:"deploymentVerification"`
}

func assessBuildReadiness(m Manifest, assets map[string][]byte) BuildReadiness {
	report := BuildReadiness{ReportVersion: 1, AnalysisScope: "literal-references-in-selected-bicep-files", Status: "not-assessed", BuildSources: []BuildReference{}, RecipeSources: []RecipeReference{},
		Recipes: []string{}, RecipePacks: []string{}, Issues: []ReadinessIssue{}, Compilation: "not-performed", RecipeRegistration: "not-checked",
		Credentials: "not-inspected", DeploymentVerification: "not-performed"}
	if m.Integration != nil {
		report.Recipes = append(report.Recipes, m.Integration.Recipes...)
		report.RecipePacks = append(report.RecipePacks, m.Integration.RecipePacks...)
	}
	add := func(code, phase, file, message, next string) {
		report.Issues = append(report.Issues, ReadinessIssue{code, phase, file, message, next})
	}
	for _, file := range sortedKeys(assets) {
		if !strings.HasSuffix(file, ".bicep") {
			continue
		}
		role := integrationRole(file, m.Integration)
		if role != "" {
			literals := literalRecipeSources(assets[file])
			for _, reference := range literals {
				state := "review-required"
				if immutableRecipeSource(reference) {
					state = "immutable-reference"
				}
				report.RecipeSources = append(report.RecipeSources, RecipeReference{file, role, state})
				if state == "review-required" {
					add("recipe-reference-unpinned-or-unrecognized", "recipes", file,
						"A recipe source is mutable or cannot be classified as an immutable reference.",
						"Pin the recipe to a reviewed commit or OCI digest and verify consumers can access it. Do not publish during create.")
				}
			}
			if role == "recipe-pack" && (len(literals) == 0 ||
				len(regexp.MustCompile(`\bsource\s*:`).FindAll(assets[file], -1)) > len(literals)) {
				add("recipe-source-not-assessed", "recipes", file,
					"Some recipe source values are not visible as literal references.",
					"Resolve imported or parameterized recipe sources through Radius and verify their immutable identity and consumer access.")
			}
			continue
		}
		for _, raw := range regexp.MustCompile(`git::[^'"\s]+`).FindAllString(string(assets[file]), -1) {
			ref := BuildReference{File: file, Status: "review-required"}
			if strings.Contains(raw, "${") {
				add("dynamic-build-source", "build", file, "A dynamic Git build source cannot be resolved statically.", "Resolve its inputs through Radius and compare with the assembled application.")
				report.BuildSources = append(report.BuildSources, ref)
				continue
			}
			u, err := url.Parse(strings.TrimPrefix(raw, "git::"))
			if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil {
				add("unrecognized-build-source", "build", file, "A Git source cannot be compared with the selected GitHub application.", "Review the source repository and build inputs.")
				report.BuildSources = append(report.BuildSources, ref)
				continue
			}
			parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "//", 2)
			repository, err := repositoryURL(parts[0])
			if err != nil {
				add("unrecognized-build-source", "build", file, "A Git repository reference could not be classified.", "Review the source repository and build inputs.")
				report.BuildSources = append(report.BuildSources, ref)
				continue
			}
			ref.Repository = repository
			if len(parts) == 2 {
				ref.Context = path.Clean(parts[1])
				if err := safePath(ref.Context); err != nil {
					ref.Context = ""
					add("invalid-build-context", "build", file, "The Git source has an unsafe or unsupported build context.", "Use a repository-relative build context.")
					report.BuildSources = append(report.BuildSources, ref)
					continue
				}
			}
			refs := u.Query()["ref"]
			if len(refs) == 1 && commitPattern.MatchString(refs[0]) {
				ref.Revision = refs[0]
			}
			if !strings.EqualFold(repository, m.Upstream.Repository) {
				add("external-build-repository", "build", file, "The build fetches another repository.", "Confirm this is an intentional dependency, not an author-owned fork required for the application.")
			} else if ref.Revision == "" {
				add("unresolved-build-revision", "build", file, "The application build does not name a full immutable commit SHA.", "Pin the intended application revision; do not silently follow a moving branch.")
			} else if ref.Revision != m.Upstream.Commit {
				add("build-revision-mismatch", "build", file,
					fmt.Sprintf("The build uses %s while the application baseline is %s.", ref.Revision, m.Upstream.Commit),
					"Verify whether the build contains the selected application source and required adaptations.")
			} else {
				ref.Status = "reference-matches"
			}
			if strings.EqualFold(repository, m.Upstream.Repository) {
				for _, operation := range m.Files {
					if operation.Operation == "retain" {
						continue
					}
					if ref.Context != "" && !strings.HasPrefix(operation.Path, ref.Context+"/") {
						continue
					}
					// Root contexts include model files too. Docker ignore rules are
					// not evaluated here, so this is deliberately conservative.
					ref.Status = "requires-source-assembly"
					add("overlay-not-in-git-build", "build", file, "The pack changes "+operation.Path+" inside the Git build context.",
						"Use a build path that assembles the pinned upstream source plus this pack, or publish the prepared source with separate approval. Current Radius Git builds do not apply Radpack overlays.")
				}
			}
			report.BuildSources = append(report.BuildSources, ref)
		}
	}
	for _, file := range report.Recipes {
		if strings.HasSuffix(file, ".tf") || strings.HasSuffix(file, ".tf.json") {
			add("terraform-module-review-required", "recipes", file, "Terraform module dependencies are not resolved by this report.",
				"Include all required module files and scripts explicitly, and validate the complete recipe through Radius.")
		}
	}
	if len(report.Recipes)+len(report.RecipePacks) > 0 {
		add("recipe-registration-unverified", "recipes", "",
			"Reusable definitions are included; registration in a consumer environment has not been checked.",
			"Use Radius to register the recipes and attach the recipe packs in the consumer's own environment; supply their inputs there.")
	}
	if len(report.BuildSources) > 0 {
		report.Status = "reference-checks-passed"
	}
	for _, issue := range report.Issues {
		if issue.Phase == "build" {
			report.Status = "review-required"
		}
	}
	for _, ref := range report.BuildSources {
		if ref.Status == "requires-source-assembly" {
			report.Status = "requires-source-assembly"
		}
	}
	if report.Status == "not-assessed" {
		add("no-literal-git-build-sources", "build", "",
			"No literal Git build sources were found in the selected application Bicep. This does not establish that there are no builds.",
			"Review parameterized builds, local contexts, and prebuilt images with the Radius tools.")
	}
	return report
}

func literalRecipeSources(data []byte) []string {
	result := []string{}
	for _, m := range regexp.MustCompile(`\bsource\s*:\s*'([^']+)'`).FindAllStringSubmatch(string(data), -1) {
		result = append(result, m[1])
	}
	return result
}
func immutableRecipeSource(source string) bool {
	if strings.Contains(source, "${") {
		return false
	}
	if strings.HasPrefix(source, "git::https://") {
		u, err := url.Parse(strings.TrimPrefix(source, "git::"))
		if err != nil || u.User != nil {
			return false
		}
		refs := u.Query()["ref"]
		return len(refs) == 1 && commitPattern.MatchString(refs[0])
	}
	// Tags can be overwritten even when they look like releases.
	return regexp.MustCompile(`^(br:)?[A-Za-z0-9][A-Za-z0-9._:/-]+@sha256:[a-f0-9]{64}$`).MatchString(source)
}
