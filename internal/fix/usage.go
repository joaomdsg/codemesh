package fix

import (
	"maps"
	"strings"
)

// Usage is what a run has used so far: which models did the work, how many
// tokens they took and what that cost. While Claude works the cost is an
// estimate from each reply's tokens at list price; Claude's own tally at the
// end replaces it.
type Usage struct {
	Model   string // the main agent's model
	Version string // Claude Code's version
	Session string
	Exact   bool // the cost is Claude's own tally, not an estimate
	Models  map[string]ModelUse
	seen    map[string]bool // replies counted: stream-json repeats one per content block
}

// ModelUse is one model's share of a run.
type ModelUse struct {
	Calls, Input, CacheWrite, CacheRead, Output int
	USD                                         float64
	Priced                                      bool // false when the model is missing from the price list
}

// USD totals the cost; all is false when some model has no price.
func (u Usage) USD() (usd float64, all bool) {
	all = true
	for _, m := range u.Models {
		usd += m.USD
		all = all && m.Priced
	}
	return usd, all
}

// Calls counts the API calls across models.
func (u Usage) Calls() int {
	n := 0
	for _, m := range u.Models {
		n += m.Calls
	}
	return n
}

func (u Usage) clone() Usage {
	u.Models = maps.Clone(u.Models)
	u.seen = nil
	return u
}

// add counts one reply, once.
func (u *Usage) add(id, model string, t tokens) {
	if u.seen == nil {
		u.seen, u.Models = map[string]bool{}, map[string]ModelUse{}
	}
	if u.Exact || id == "" || u.seen[id] {
		return
	}
	u.seen[id] = true
	m := u.Models[model]
	m.Calls++
	m.Input += t.Input
	m.CacheWrite += t.CacheWrite
	m.CacheRead += t.CacheRead
	m.Output += t.Output
	p, ok := priceOf(model)
	m.Priced = ok
	if ok {
		m.USD += p.of(t)
	}
	u.Models[model] = m
}

// settle replaces the estimate with Claude's tally.
func (u *Usage) settle(by map[string]tally) {
	if len(by) == 0 {
		return
	}
	models := map[string]ModelUse{}
	for name, t := range by {
		models[name] = ModelUse{Calls: u.Models[name].Calls, Input: t.Input, CacheWrite: t.CacheWrite, CacheRead: t.CacheRead,
			Output: t.Output, USD: t.USD, Priced: true}
	}
	u.Models, u.Exact = models, true
}

// tokens is a reply's usage as stream-json reports it.
type tokens struct {
	Input      int `json:"input_tokens"`
	CacheWrite int `json:"cache_creation_input_tokens"`
	CacheRead  int `json:"cache_read_input_tokens"`
	Output     int `json:"output_tokens"`
	Cache      struct {
		M5 int `json:"ephemeral_5m_input_tokens"`
		H1 int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

// tally is one model's total in Claude's final result.
type tally struct {
	Input      int     `json:"inputTokens"`
	Output     int     `json:"outputTokens"`
	CacheRead  int     `json:"cacheReadInputTokens"`
	CacheWrite int     `json:"cacheCreationInputTokens"`
	USD        float64 `json:"costUSD"`
}

// price is a model's list price in USD per million tokens, from
// https://platform.claude.com/docs/en/about-claude/pricing (October 2026).
// It only drives the live estimate. A request whose prompt, cache included,
// is over long tokens costs longX times as much.
type price struct {
	in, write5m, write1h, read, out float64
	long                            int
	longX                           float64
}

var prices = map[string]price{
	"claude-fable-5-1":  {in: 10, write5m: 12.50, write1h: 20, read: 0.25, out: 50},
	"claude-opus-5-5":   {in: 4, write5m: 5, write1h: 8, read: 0.20, out: 20},
	"claude-sonnet-5-5": {in: 2, write5m: 2.50, write1h: 4, read: 0.10, out: 10},
	"claude-haiku-5-5":  {in: 0.10, write5m: 0.125, write1h: 0.20, read: 0.01, out: 0.50, long: 100_000, longX: 5},
	"claude-haiku-4-5":  {in: 1, write5m: 1.25, write1h: 2, read: 0.10, out: 5},
}

// priceOf finds a model's price; a dated ID such as
// claude-haiku-4-5-20251001 takes its model's.
func priceOf(model string) (price, bool) {
	for id, p := range prices {
		if model == id || strings.HasPrefix(model, id+"-") {
			return p, true
		}
	}
	return price{}, false
}

func (p price) of(t tokens) float64 {
	x := 1.0
	if p.long > 0 && t.Input+t.CacheWrite+t.CacheRead > p.long {
		x = p.longX
	}
	// A write without a 5m/1h split is billed as 5m.
	m5, h1 := t.Cache.M5, t.Cache.H1
	if m5+h1 == 0 {
		m5 = t.CacheWrite
	}
	return x * (float64(t.Input)*p.in + float64(m5)*p.write5m + float64(h1)*p.write1h + float64(t.CacheRead)*p.read + float64(t.Output)*p.out) / 1e6
}
