package ui

import (
	"html"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/engine"
)

// TestFreeAlwaysKeptMatchesDefaults pins Free up space's Always kept list
// to engine.DefaultExcludes: each pattern is named once, in a line that
// says in plain words what it keeps, so a pattern added to or dropped from
// the defaults fails here until the page says so.
func TestFreeAlwaysKeptMatchesDefaults(t *testing.T) {
	b, err := assets.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	start := strings.Index(page, `<details class="help" id="x-always">`)
	if start < 0 {
		t.Fatal("index.html has no Always kept list")
	}
	end := strings.Index(page[start:], "</details>")
	if end < 0 {
		t.Fatal("the Always kept list is not closed")
	}
	block := page[start : start+end]
	if !strings.Contains(block, "<summary>Always kept</summary>") {
		t.Error("the Always kept list lacks its summary")
	}
	// It sits with the pattern fields, before Keep on the telescope.
	if field := strings.Index(page, `<label for="x-ex">`); field < start {
		t.Error("the Always kept list is not just before Keep on the telescope")
	}

	var named []string
	items := regexp.MustCompile(`<li>([^<:]+): (.*?)</li>`).FindAllStringSubmatch(block, -1)
	if len(items) != strings.Count(block, "<li>") {
		t.Fatalf("a line of the Always kept list does not read \"words: patterns\": %q", block)
	}
	for _, item := range items {
		if strings.TrimSpace(item[1]) == "" {
			t.Errorf("a line names patterns without words: %q", item[0])
		}
		for _, code := range regexp.MustCompile(`<code>(.*?)</code>`).FindAllStringSubmatch(item[2], -1) {
			named = append(named, html.UnescapeString(code[1]))
		}
	}
	want := slices.Clone(engine.DefaultExcludes)
	slices.Sort(want)
	slices.Sort(named)
	if !slices.Equal(named, want) {
		t.Errorf("Always kept names %q, engine.DefaultExcludes is %q", named, want)
	}
}
