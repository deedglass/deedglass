package docs_test

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/policy"
)

// TestCookbook keeps docs/POLICY_COOKBOOK.md honest (S12 §8): it has exactly 17 recipes, each with
// one policy that compiles with zero errors and zero warnings under --voice, and each recipe's
// cases block passes against that policy.
func TestCookbook(t *testing.T) {
	raw, err := os.ReadFile("POLICY_COOKBOOK.md")
	if err != nil {
		t.Fatal(err)
	}
	recipes := splitRecipes(string(raw))
	if len(recipes) != 17 {
		t.Fatalf("found %d recipes (headings like \"## 1. …\"); want 17", len(recipes))
	}
	policies := 0
	for _, b := range yamlBlocks(string(raw)) {
		if strings.HasPrefix(b, "deedglass: policy/v1") {
			policies++
		}
	}
	if policies != 17 {
		t.Errorf("found %d policy blocks in the whole file; want 17, one per recipe", policies)
	}
	for i, r := range recipes {
		if r.num != i+1 {
			t.Errorf("recipe %q is numbered %d; want %d", r.title, r.num, i+1)
		}
		t.Run(r.title, func(t *testing.T) { checkRecipe(t, r) })
	}
}

type recipe struct {
	num   int
	title string
	body  string
}

var recipeHeading = regexp.MustCompile(`(?m)^## (\d+)\. (.+)$`)

func splitRecipes(doc string) []recipe {
	locs := recipeHeading.FindAllStringSubmatchIndex(doc, -1)
	var out []recipe
	for i, l := range locs {
		end := len(doc)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		// A later "## " heading that isn't a recipe ends the last recipe.
		body := doc[l[1]:end]
		if j := strings.Index(body, "\n## "); j >= 0 {
			body = body[:j]
		}
		n := 0
		for _, c := range doc[l[2]:l[3]] {
			n = n*10 + int(c-'0')
		}
		out = append(out, recipe{num: n, title: doc[l[4]:l[5]], body: body})
	}
	return out
}

// yamlBlocks returns the contents of every ```yaml fenced block.
func yamlBlocks(md string) []string {
	var out []string
	var cur []string
	in := false
	for _, line := range strings.Split(md, "\n") {
		switch {
		case !in && strings.TrimSpace(line) == "```yaml":
			in, cur = true, nil
		case in && strings.TrimSpace(line) == "```":
			in = false
			out = append(out, strings.Join(cur, "\n")+"\n")
		case in:
			cur = append(cur, line)
		}
	}
	return out
}

func checkRecipe(t *testing.T, r recipe) {
	var src, cases string
	for _, b := range yamlBlocks(r.body) {
		switch {
		case strings.HasPrefix(b, "deedglass: policy/v1"):
			if src != "" {
				t.Fatal("more than one policy block")
			}
			src = b
		case strings.HasPrefix(b, "cases:"):
			if cases != "" {
				t.Fatal("more than one cases block")
			}
			cases = b
		}
	}
	if src == "" {
		t.Fatal("no ```yaml block starting with deedglass: policy/v1")
	}
	c, diags := policy.Compile([]byte(src), policy.Options{File: "recipe.yaml", Voice: true})
	for _, d := range diags {
		t.Errorf("%s line %d: %s: %s %s", d.Severity, d.Line, d.Code, d.Message, d.Hint)
	}
	if c == nil {
		t.Fatal("the policy was not accepted")
	}
	if cases == "" {
		t.Fatal("no ```yaml block starting with cases:")
	}
	cf, diags := policy.ParseCasesYAML([]byte(cases), "recipe.cases.yaml")
	for _, d := range diags {
		t.Errorf("cases line %d: %s: %s %s", d.Line, d.Code, d.Message, d.Hint)
	}
	if cf == nil {
		t.Fatal("the cases were not accepted")
	}
	res := policy.RunCases(context.Background(), c, cf, policy.TestOptions{})
	for _, cr := range res.Cases {
		for _, p := range cr.Problems {
			t.Errorf("case %q: %s", cr.Name, p)
		}
	}
	if !res.OK {
		t.Errorf("%d of %d cases failed", res.Failed, len(res.Cases))
	}
}
