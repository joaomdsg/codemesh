package ui_test

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/joaomdsg/codemesh/internal/handoff"
	"github.com/joaomdsg/codemesh/internal/testrepo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// control matches a button with its text, or the opening tag of a form or input.
var control = regexp.MustCompile(`<(?:button|form|input)\b[^>]*>(?:[^<]*</button>)?`)

// basketPressing finds the n-th control on path whose markup holds label and has an
// action bound, and returns a func that fires it. Controls without an action,
// such as a disabled button, are skipped.
func (e env) basketPressing(t *testing.T, path, label string, n int, body string) func() {
	t.Helper()
	conn := e.app.ConnectAt(path, "{}")
	t.Cleanup(conn.Close)
	_, page := e.app.Get(path)
	for _, c := range control.FindAllString(page, -1) {
		m := actionURL.FindStringSubmatch(c)
		if m == nil || !strings.Contains(c, label) {
			continue
		}
		if n--; n >= 0 {
			continue
		}
		return func() {
			t.Helper()
			status, _ := e.app.ChildAction("r", 1).Over(conn).Raw(html.UnescapeString(m[2] + m[1])).Body(body).Fire()
			require.Equal(t, http.StatusNoContent, status)
		}
	}
	t.Fatalf("no %q control #%d on %s", label, n, path)
	return nil
}

func (e env) basketPress(t *testing.T, path, label string) {
	t.Helper()
	e.basketPressing(t, path, label, 0, "{}")()
}

func (e env) basketTitles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, it := range e.a.Basket().Items {
		out = append(out, it.Title)
	}
	return out
}

func (e env) basketText(t *testing.T) string {
	t.Helper()
	status, body := e.app.Get("/basket.txt")
	require.Equal(t, http.StatusOK, status)
	return body
}

func (e env) addToBasket(t *testing.T, kind handoff.Kind, title string) {
	t.Helper()
	_, err := e.a.BasketAdd(handoff.Item{Kind: kind, Repo: e.repo.ID, Title: title, Where: "calc/calc.go:1"})
	require.NoError(t, err)
}

func TestBasket_saysWhatToDoWhenEmpty(t *testing.T) {
	e := serve(t)

	status, page := e.app.Get("/basket")
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, page, "Add a finding from a repo")
	assert.Equal(t, "The basket is empty.\n", e.basketText(t))
}

func TestBasketText_isPlainText(t *testing.T) {
	e := serve(t)
	e.addToBasket(t, handoff.Finding, "Split Clamp")
	srv := e.app.Client()

	resp, err := srv.Get(e.app.URL() + "/basket.txt")
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/plain; charset=utf-8", resp.Header.Get("Content-Type"))
}

func TestMap_addsASmellToTheHandoff(t *testing.T) {
	e := serve(t)

	e.basketPress(t, e.base()+"/", "Add to handoff")

	items := e.a.Basket().Items
	require.Len(t, items, 1)
	assert.Equal(t, handoff.Finding, items[0].Kind)
	assert.Equal(t, e.repo.ID, items[0].Repo)
	text := e.basketText(t)
	assert.Contains(t, text, "Complex function")
	assert.Contains(t, text, "Where: `calc/calc.go:")
	assert.Contains(t, text, "Scope: working tree")
	assert.Contains(t, text, "func tangle(x int) int {", "the declaration's code rides along")
	_, page := e.app.Get(e.base() + "/")
	assert.Contains(t, page, "In handoff", "the finding shows it is in the basket")
	assert.NotContains(t, page, ">Add to handoff<", "and offers no second add")
	assert.Regexp(t, `href="/basket"[^>]*>Basket<span class="count">1</span>`, page)
}

func TestHealth_addsAPrognosisToTheHandoffFromTheListAndFromItsPanel(t *testing.T) {
	key := "complex:example.com/calc/calc.tangle"
	for name, path := range map[string]string{"list": "/health", "panel": "/prognosis/" + url.PathEscape(key)} {
		t.Run(name, func(t *testing.T) {
			e := serve(t)

			e.basketPress(t, e.base()+path, "Add to handoff")

			items := e.a.Basket().Items
			require.Len(t, items, 1)
			assert.Equal(t, handoff.Prognosis, items[0].Kind)
			assert.Equal(t, key, items[0].Key)
			text := e.basketText(t)
			assert.Contains(t, text, "Steps:\n1. ", "the prognosis's steps")
			assert.Contains(t, text, "Check: ", "and how to check")
			assert.Contains(t, text, "Where: `calc/calc.go:")
		})
	}
}

func TestBasket_listsItemsByRepoThenKind(t *testing.T) {
	e := serveDirs(t, testrepo.New(t, testrepo.CalcBase, nil), testrepo.New(t, testrepo.CalcBase, nil))
	for _, r := range e.repos {
		for _, k := range []handoff.Kind{handoff.Note, handoff.Finding} {
			_, err := e.a.BasketAdd(handoff.Item{Kind: k, Repo: r.ID, Title: string(k) + " in " + r.Name})
			require.NoError(t, err)
		}
	}

	_, page := e.app.Get("/basket")

	var at []int
	for _, r := range e.repos {
		for _, k := range []handoff.Kind{handoff.Finding, handoff.Note} {
			i := strings.Index(page, string(k)+" in "+r.Name)
			require.GreaterOrEqual(t, i, 0, "%s of %s", k, r.Name)
			at = append(at, i)
		}
	}
	assert.IsIncreasing(t, at, "repos in name order, findings before notes")
	assert.Contains(t, page, ">Findings<")
	assert.Contains(t, page, ">Review notes<")
}

func TestBasket_removesAnItem(t *testing.T) {
	e := serve(t)
	e.addToBasket(t, handoff.Finding, "first")
	e.addToBasket(t, handoff.Finding, "second")

	e.basketPress(t, "/basket", "Remove")

	assert.Equal(t, []string{"second"}, e.basketTitles(t))
}

func TestBasket_movesAnItemWithinItsGroup(t *testing.T) {
	e := serve(t)
	e.addToBasket(t, handoff.Finding, "first")
	e.addToBasket(t, handoff.Note, "between")
	e.addToBasket(t, handoff.Finding, "second")

	e.basketPress(t, "/basket", "Move up")

	assert.Equal(t, []string{"second", "first", "between"}, e.basketTitles(t), "the note between them is not a stop")
	text := e.basketText(t)
	assert.Less(t, strings.Index(text, "#### second"), strings.Index(text, "#### first"))

	e.basketPress(t, "/basket", "Move down")

	assert.Equal(t, []string{"first", "second", "between"}, e.basketTitles(t))
}

func TestBasket_offersNoMoveBeyondTheEndsOfAGroup(t *testing.T) {
	e := serve(t)
	e.addToBasket(t, handoff.Finding, "only")

	_, page := e.app.Get("/basket")

	for _, label := range []string{`aria-label="Move up"`, `aria-label="Move down"`} {
		var tag string
		for _, c := range control.FindAllString(page, -1) {
			if strings.Contains(c, label) {
				tag = c
			}
		}
		assert.Contains(t, tag, "disabled", label)
	}
}

func TestBasket_putsTheIntroAtTheTopOfThePrompt(t *testing.T) {
	e := serve(t)
	e.addToBasket(t, handoff.Finding, "Split Clamp")

	e.basketPressing(t, "/basket", "intro", 0, `{"intro":"Make Clamp easier to read."}`)()

	assert.Contains(t, e.basketText(t), "Goal: Make Clamp easier to read.")
	_, page := e.app.Get("/basket")
	assert.Contains(t, page, "Make Clamp easier to read.", "the field and the preview show it")
}

func TestBasket_copyWaitsForTheGoalTheServerHasNotSavedYet(t *testing.T) {
	e := serve(t)
	e.addToBasket(t, handoff.Finding, "Split Clamp")
	e.basketPressing(t, "/basket", "intro", 0, `{"intro":"Make Clamp easier to read."}`)()

	_, page := e.app.Get("/basket")

	assert.Contains(t, page, `data-goal="Make Clamp easier to read."`, "what the preview was built from")
	assert.Contains(t, page, "dataset.goal", "Copy compares it with the field before copying: the field saves on blur, which Copy's own click causes")
}

func TestBasket_previewsThePromptAndCopiesIt(t *testing.T) {
	e := serve(t)
	e.addToBasket(t, handoff.Finding, "Split Clamp")

	_, page := e.app.Get("/basket")

	assert.Regexp(t, `<textarea[^>]*readonly[^>]*>[^<]*## When you are done`, page, "a selectable copy of the prompt")
	assert.Contains(t, page, "navigator.clipboard")
	assert.Contains(t, page, ">Copy<")
}

func TestBasket_saysTheScopeInWordsNotAsASHA(t *testing.T) {
	e := serve(t)
	for i, scope := range []string{"pr:178@78c558013849a9858e1e27c2de53e44d1893f79a", "wt", "commit:abc1234"} {
		_, err := e.a.BasketAdd(handoff.Item{Kind: handoff.Finding, Repo: e.repo.ID, Title: fmt.Sprint("item ", i), Where: "a.go:1", Scope: scope})
		require.NoError(t, err)
	}

	_, page := e.app.Get("/basket")

	assert.Contains(t, page, `class="chip">PR #178 @78c5580<`)
	assert.Contains(t, page, `class="chip">working tree<`)
	assert.Contains(t, page, `class="chip">commit abc1234<`)
	assert.NotContains(t, page, "78c558013849a9858e1e27c2de53e44d1893f79a</span>")
}
