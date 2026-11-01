package radpack

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	for _, value := range values {
		seen[value] = true
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func integrationRole(file string, integration *IntegrationAssets) string {
	if integration == nil {
		return ""
	}
	for _, entry := range integration.RecipePacks {
		if entry == file {
			return "recipe-pack"
		}
	}
	for _, entry := range integration.Recipes {
		if entry == file {
			return "recipe"
		}
	}
	return ""
}

func validateIntegration(m Manifest) error {
	if m.FormatVersion == 1 {
		if m.Integration != nil {
			return fmt.Errorf("recipe roles require pack format version 2")
		}
		return nil
	}
	if m.Integration == nil || m.Integration.Recipes == nil || m.Integration.RecipePacks == nil {
		return fmt.Errorf("format version 2 requires integration.recipes and integration.recipePacks arrays")
	}
	roles := append(append([]string{}, m.Integration.Recipes...), m.Integration.RecipePacks...)
	if len(roles) == 0 || len(roles) > 1000 {
		return fmt.Errorf("integration must declare between 1 and 1000 recipe entry points")
	}
	seen := map[string]bool{}
	for _, file := range roles {
		if err := safePath(file); err != nil {
			return err
		}
		if file == m.Application {
			return fmt.Errorf("application model cannot also be a recipe entry point: %s", file)
		}
		key := strings.ToLower(file)
		if seen[key] {
			return fmt.Errorf("duplicate or conflicting integration role: %s", file)
		}
		seen[key] = true
		found := false
		for _, entry := range m.Files {
			if entry.Path == file && entry.After != nil {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("integration entry point must be a declared installed file: %s", file)
		}
		if integrationRole(file, m.Integration) == "recipe-pack" {
			if !strings.HasSuffix(file, ".bicep") {
				return fmt.Errorf("recipe-pack entry points must be Bicep files: %s", file)
			}
		} else if !strings.HasSuffix(file, ".bicep") && !strings.HasSuffix(file, ".tf") && !strings.HasSuffix(file, ".tf.json") {
			return fmt.Errorf("recipe entry points must be Bicep or Terraform files: %s", file)
		}
	}
	return nil
}

func integrationExcludedReason(file string, integration *IntegrationAssets) (string, error) {
	if integrationRole(file, integration) == "recipe-pack" && path.Base(file) == "radius-recipe-pack.bicep" {
		// The generated filename is not inherently unsafe. Only an explicitly
		// declared reusable pack may use it; all parent-path restrictions remain.
		return excludedReason(path.Join(path.Dir(file), "custom-recipe-pack.bicep"))
	}
	return excludedReason(file)
}

var recipePackDeclaration = regexp.MustCompile(`(?m)^\s*resource\s+[A-Za-z_][A-Za-z0-9_]*\s+'Radius\.Core/recipePacks@[^']+'\s*=\s*\{`)

func assertIntegrationAsset(data []byte, file string, integration *IntegrationAssets) error {
	if integrationRole(file, integration) != "recipe-pack" {
		return assertPortableFile(data, file)
	}
	reason, err := integrationExcludedReason(file, integration)
	if err != nil {
		return err
	}
	if reason != "" {
		return fmt.Errorf("cannot package %s: %s", file, reason)
	}
	findings, err := inspectContent(data, file)
	if err != nil {
		return err
	}
	if len(findings) > 0 {
		return fmt.Errorf("cannot package %s: %s. Values withheld; use consumer-supplied inputs", file, strings.Join(findings, ", "))
	}
	if !recipePackDeclaration.Match(data) {
		return fmt.Errorf("declared recipe pack %s must contain a Radius.Core/recipePacks resource; this check does not replace Bicep validation", file)
	}
	return nil
}
