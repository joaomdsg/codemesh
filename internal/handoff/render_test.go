package handoff

import (
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs from %s\n--- got ---\n%s--- want ---\n%s", name, path, got, want)
	}
}

var (
	finding = Item{
		Kind: Finding, Repo: "shop", Scope: "wt",
		Title: "Cart.Total has cyclomatic complexity 31",
		Where: "cart/cart.go:42",
		Body:  "The method branches on every discount type.",
		Code:  []string{"func (c *Cart) Total() int {", "\tif c.vip {", "\t\t...", "}"},
		Key:   "shop/cart.Cart.Total@a1b2c3",
	}
	prognosis = Item{
		Kind: Prognosis, Repo: "shop", Scope: "wt",
		Title: "Split Cart.Total by discount type",
		Where: "cart/cart.go:42",
		Body:  "Three discount branches share one loop.",
		Steps: []string{"Extract each discount into its own function.", "Keep Total as the dispatcher."},
		Check: "go test ./cart passes and Total's complexity is under 10.",
		Key:   "shop/cart.Cart.Total@a1b2c3",
	}
	note = Item{
		Kind: Note, Repo: "shop", Scope: "pr:7@9f8e7d6",
		Title: "Rounding drops a cent",
		Where: "store/store.go:17",
		Body:  "Use integer cents here, not float64.",
		Code:  []string{"+\tprice := float64(n) * 0.1"},
		Key:   "shop/store.Price@c0ffee",
	}
	thread = Item{
		Kind: Thread, Repo: "shop", Scope: "pr:7@9f8e7d6",
		Title: "Blast radius of store.Price",
		Where: "store/store.go:17",
		Body:  "store.Price\n<- cart.Cart.Total (caller)\n<- cmd/shop.main (caller)",
		Key:   "shop/store.Price@c0ffee",
	}
	introduced = Item{
		Kind: Introduced, Repo: "shop", Scope: "pr:7@9f8e7d6",
		Title: "New dead code: store.legacyPrice",
		Where: "store/store.go:60",
		Body:  "Nothing calls it.",
		Check: "The finding is gone from the PR compare.",
		Key:   "dead-code:shop/store.legacyPrice",
	}
	other = Item{
		Kind: Finding, Repo: "blog", Scope: "wt",
		Title: "Unused export Render",
		Where: "src/page.ts:3",
		Body:  "Matched by name, so it may be used dynamically.",
		Key:   "blog/page.Render@dd01",
	}
)

func TestRender(t *testing.T) {
	names := map[string]string{"shop": "Shop", "blog": "Blog"}
	cases := []struct {
		name  string
		intro string
		items []Item
		ctx   Context
	}{
		{"empty", "", nil, Context{}},
		{"finding", "", []Item{finding}, Context{Repos: names, Branch: "main"}},
		{"prognosis", "", []Item{prognosis}, Context{Repos: names}},
		{"note", "", []Item{note}, Context{Repos: names}},
		{"thread", "", []Item{thread}, Context{Repos: names}},
		{"introduced", "", []Item{introduced}, Context{Repos: names}},
		{"intro", "Make Cart.Total readable without changing what it returns.", []Item{finding, prognosis}, Context{Repos: names}},
		{"pr", "", []Item{note, introduced}, Context{Repos: names, Branch: "fix-price", PR: "#7 Fix price rounding", BaseSHA: "1a2b3c4", HeadSHA: "9f8e7d6"}},
		{"mixed", "", []Item{thread, other, finding, note, prognosis, introduced}, Context{Repos: names}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			golden(t, c.name, Render(Basket{Intro: c.intro, Items: c.items}, c.ctx))
		})
	}
}

func TestRender_endsWithInstructionsToVerifyAndRunTests(t *testing.T) {
	got := Render(Basket{Items: []Item{finding}}, Context{})
	i := strings.Index(got, "## When you are done")
	if i < 0 {
		t.Fatalf("no closing instructions:\n%s", got)
	}
	for _, want := range []string{"verify", "run the tests", "skip"} {
		if !strings.Contains(strings.ToLower(got[i:]), want) {
			t.Errorf("closing instructions do not mention %q:\n%s", want, got[i:])
		}
	}
	if strings.Index(got, "#### Cart.Total") > i {
		t.Error("the instructions come before the items")
	}
}

func TestRender_dropsTheIntroWhenBlank(t *testing.T) {
	blank := Render(Basket{Intro: "  \t", Items: []Item{finding}}, Context{})
	none := Render(Basket{Items: []Item{finding}}, Context{})
	if blank != none {
		t.Errorf("a blank intro changed the prompt:\n%s", blank)
	}
}

func TestRender_ignoresTheOrderReposWereAddedIn(t *testing.T) {
	ctx := Context{Repos: map[string]string{"shop": "Shop", "blog": "Blog"}}
	a := Item{Kind: Finding, Repo: "shop", Title: "a", Where: "x.go:1"}
	b := Item{Kind: Finding, Repo: "blog", Title: "b", Where: "x.go:1"}
	one := Render(Basket{Items: []Item{a, b}}, ctx)
	two := Render(Basket{Items: []Item{b, a}}, ctx)
	if one != two {
		t.Errorf("the order repos were added in changed the prompt\n--- one ---\n%s--- two ---\n%s", one, two)
	}
}

func TestRender_keepsTheBasketOrderWithinAKind(t *testing.T) {
	early := Item{Kind: Finding, Repo: "shop", Title: "first added", Where: "z/z.go:1"}
	late := Item{Kind: Finding, Repo: "shop", Title: "second added", Where: "a/a.go:1"}
	sameFile := Item{Kind: Finding, Repo: "shop", Title: "third added", Where: "a/a.go:9"}
	got := Render(Basket{Items: []Item{early, late, sameFile}}, Context{})
	want := []string{"first added", "second added", "third added"}
	var order []string
	for _, line := range strings.Split(got, "\n") {
		if t, ok := strings.CutPrefix(line, "#### "); ok {
			order = append(order, t)
		}
	}
	if !slices.Equal(order, want) {
		t.Errorf("item order = %q, want %q (the order the user gave)", order, want)
	}
}

func TestRender_fencesCodeContainingFences(t *testing.T) {
	it := Item{Kind: Finding, Repo: "r", Title: "t", Code: []string{"```go", "x := 1", "```"}}
	got := Render(Basket{Items: []Item{it}}, Context{})
	if !strings.Contains(got, "````\n```go\nx := 1\n```\n````\n") {
		t.Errorf("code fence does not outgrow the fences inside the code:\n%s", got)
	}
}

func TestStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "basket.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Basket().Items; len(got) != 0 {
		t.Fatalf("new store has items: %v", got)
	}
	var ids []string
	for _, it := range []Item{finding, prognosis, note} {
		added, err := s.Add(it)
		if err != nil {
			t.Fatal(err)
		}
		if added.ID == "" {
			t.Fatal("Add left the ID empty")
		}
		ids = append(ids, added.ID)
	}
	titles := func(s *Store) []string {
		var out []string
		for _, it := range s.Basket().Items {
			out = append(out, it.Title)
		}
		return out
	}
	if err := s.Move(ids[2], 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(ids[1]); err != nil {
		t.Fatal(err)
	}
	want := []string{note.Title, finding.Title}
	if got := titles(s); !slices.Equal(got, want) {
		t.Errorf("after move and remove: %q, want %q", got, want)
	}
	re, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := titles(re); !slices.Equal(got, want) {
		t.Errorf("after reopen: %q, want %q", got, want)
	}
	if got := re.Basket().Items[0]; got.Code[0] != note.Code[0] || got.Key != note.Key {
		t.Errorf("reopened item lost its code or key: %+v", got)
	}
	if err := s.Move("nope", 0); err == nil {
		t.Error("Move of an unknown ID returned nil")
	}
	if err := s.Remove("nope"); err != nil {
		t.Errorf("Remove of an unknown ID: %v", err)
	}
}

func TestGroups_listReposByNameThenKindsInPromptOrder(t *testing.T) {
	ctx := Context{Repos: map[string]string{"shop": "Shop", "blog": "Blog"}}
	b := Basket{Items: []Item{finding, other, introduced, note, prognosis}}

	var got []string
	for _, g := range Groups(b, ctx) {
		var titles []string
		for _, it := range g.Items {
			titles = append(titles, it.Title)
		}
		got = append(got, g.Repo+"/"+g.Heading+": "+strings.Join(titles, ","))
	}
	want := []string{
		"Blog/Findings: " + other.Title,
		"Shop/Introduced by this change: " + introduced.Title,
		"Shop/Findings: " + finding.Title,
		"Shop/Prognoses: " + prognosis.Title,
		"Shop/Review notes: " + note.Title,
	}
	if !slices.Equal(got, want) {
		t.Errorf("groups:\n%q\nwant\n%q", got, want)
	}
}

func TestStore_keepsTheIntro(t *testing.T) {
	path := filepath.Join(t.TempDir(), "basket.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetIntro("Clean up the cart."); err != nil {
		t.Fatal(err)
	}
	re, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := re.Basket().Intro; got != "Clean up the cart." {
		t.Errorf("intro after reopen = %q", got)
	}
}
