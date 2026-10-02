package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Least used puts first the account with the most of its week left per
// hour until it renews — the one with the most to lose at its reset — not
// the one with the most left: 20% left and an hour to go beats 60% left
// and five days. Alike within a tenth go by the tokens sent lately, then
// keep their order; one all but used up goes last whatever its week, by
// how far; one not known counts as a fresh week, as does one whose vendor
// tells the five hours alone, with their share used.
//
// PIN (2026-10-01): weekly pace replaces most-left-first under the saved
// id "usage"; Smart is left as it is.
func TestLeastUsedPace(t *testing.T) {
	old := allowances
	defer func() { allowances = old }()
	now := time.Now()
	share := map[string]provider.Allowance{}
	allowances = func(string) map[string]provider.Allowance { return share }
	p := provider.Provider{ID: "codex", Routing: provider.LeastUsed, Account: &provider.Account{Agent: "codex", User: "a@x.com"}}
	acct := func(user string) candidate {
		return candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: user}}, model: "gpt-5.6-sol", rest: "codex#" + user}
	}
	week := func(used float64, in time.Duration) provider.Allowance {
		return provider.Allowance{
			{Used: 10, Resets: now.Add(time.Hour), Span: 5 * time.Hour},
			{Used: used, Resets: now.Add(in), Span: 7 * 24 * time.Hour},
		}
	}
	order := func(cs ...candidate) string {
		got, _ := weigh(p, cs, "gpt-5.6-sol", provider.Chat)
		var s []string
		for _, c := range got {
			s = append(s, c.p.Account.User[:1])
		}
		return strings.Join(s, "")
	}

	// the owner's case: 80% used renewing in an hour has the most to lose
	share["a@x.com"] = week(40, 5*24*time.Hour) // 60 left / 120 h = 0.5
	share["b@x.com"] = week(80, time.Hour)      // 20 left / 1 h = 20
	if got := order(acct("a@x.com"), acct("b@x.com")); got != "ba" {
		t.Fatalf("imminent renewal first: %s", got)
	}
	// renewing the same hour, pace is most left first, as before
	share["a@x.com"] = week(30, 76*time.Hour)
	share["b@x.com"] = week(70, 76*time.Hour)
	if got := order(acct("b@x.com"), acct("a@x.com")); got != "ab" {
		t.Fatalf("same deadline, most left first: %s", got)
	}
	// within a tenth alike: their order, for the cache
	share["a@x.com"] = week(50, 76*time.Hour)
	share["b@x.com"] = week(54, 76*time.Hour)
	if got := order(acct("b@x.com"), acct("a@x.com")); got != "ba" {
		t.Fatalf("alike keep their order: %s", got)
	}
	// all but used up: last, though its pace is the highest
	share["a@x.com"] = week(40, 5*24*time.Hour)
	share["b@x.com"] = week(98, time.Hour) // 2 left / 1 h = 2 > 0.5
	if got := order(acct("b@x.com"), acct("a@x.com")); got != "ab" {
		t.Fatalf("spent last: %s", got)
	}
	// Opus's own week decides an Opus request; Sonnet's goes by the general
	share["a@x.com"] = provider.Allowance{
		{Used: 10, Resets: now.Add(2 * 24 * time.Hour), Span: 7 * 24 * time.Hour},
		{Used: 95, Resets: now.Add(2 * 24 * time.Hour), Span: 7 * 24 * time.Hour, Model: "opus"},
	}
	share["b@x.com"] = provider.Allowance{
		{Used: 60, Resets: now.Add(2 * 24 * time.Hour), Span: 7 * 24 * time.Hour},
		{Used: 60, Resets: now.Add(2 * 24 * time.Hour), Span: 7 * 24 * time.Hour, Model: "opus"},
	}
	opus := func(user string) candidate {
		c := acct(user)
		c.model = "claude-opus-5-5"
		return c
	}
	if got, _ := weigh(p, []candidate{opus("a@x.com"), opus("b@x.com")}, "claude-opus-5-5", provider.Anthropic); got[0].p.Account.User != "b@x.com" {
		t.Fatalf("Opus request goes by the Opus week: %s first", got[0].p.Account.User)
	}
	if got := order(acct("b@x.com"), acct("a@x.com")); got != "ab" {
		t.Fatalf("a Sonnet request goes by the general week: %s", got)
	}
	// one not known counts as a fresh week: ahead of one half through its,
	// behind one about to lose what it has
	delete(share, "c@x.com")
	share["a@x.com"] = week(40, 5*24*time.Hour)
	share["b@x.com"] = week(80, time.Hour)
	if got := order(acct("a@x.com"), acct("c@x.com"), acct("b@x.com")); got != "bca" {
		t.Fatalf("not known as a fresh week: %s", got)
	}
	// its five hours all but used up, its week healthy: last all the same
	share["a@x.com"] = provider.Allowance{
		{Used: 98, Resets: now.Add(time.Hour), Span: 5 * time.Hour},
		{Used: 10, Resets: now.Add(time.Hour), Span: 7 * 24 * time.Hour},
	}
	share["b@x.com"] = week(60, 5*24*time.Hour)
	if got := order(acct("a@x.com"), acct("b@x.com")); got != "ba" {
		t.Fatalf("five hours spent: %s", got)
	}
	// both spent: the one with the least used first, it may yet answer
	share["b@x.com"] = provider.Allowance{{Used: 100, Resets: now.Add(time.Hour), Span: 5 * time.Hour}, {Used: 10, Resets: now.Add(time.Hour), Span: 7 * 24 * time.Hour}}
	if got := order(acct("b@x.com"), acct("a@x.com")); got != "ab" {
		t.Fatalf("spent by how far: %s", got)
	}
	// the vendor tells the five hours alone (Claude's own usage command):
	// by what they have used, as before, a fresh one as one not known
	share["a@x.com"] = provider.Allowance{{Used: 97, Resets: now.Add(time.Hour), Span: 5 * time.Hour}}
	share["b@x.com"] = provider.Allowance{{Used: 5, Resets: now.Add(time.Hour), Span: 5 * time.Hour}}
	share["c@x.com"] = provider.Allowance{{Used: 0, Resets: now.Add(time.Hour), Span: 5 * time.Hour}}
	delete(share, "d@x.com")
	if got := order(acct("a@x.com"), acct("d@x.com"), acct("c@x.com"), acct("b@x.com")); got != "dcba" {
		t.Fatalf("five hours alone go by their share used, the fresh with the unknown: %s", got)
	}
	// three paces a twelfth apart: alike pairwise all round, yet an order
	// — bands drawn from the top, so the first two are alike and the
	// third is not; within a band, the tokens sent lately decide
	share["a@x.com"] = week(0, 100*time.Hour)  // 1.00
	share["b@x.com"] = week(9, 100*time.Hour)  // 0.91
	share["c@x.com"] = week(17, 100*time.Hour) // 0.83
	routed.Lock()
	routed.used[acct("a@x.com").restKey()] = tokenUse{3000, now}
	routed.used[acct("b@x.com").restKey()] = tokenUse{2000, now}
	routed.used[acct("c@x.com").restKey()] = tokenUse{1000, now}
	routed.Unlock()
	if got := order(acct("a@x.com"), acct("b@x.com"), acct("c@x.com")); got != "bac" {
		t.Fatalf("bands from the top, tokens within: %s", got)
	}
	if got := order(acct("c@x.com"), acct("b@x.com"), acct("a@x.com")); got != "bac" {
		t.Fatalf("the same from the other end: %s", got)
	}
	routed.Lock()
	for _, u := range []string{"a@x.com", "b@x.com", "c@x.com"} {
		delete(routed.used, acct(u).restKey())
	}
	routed.Unlock()
}
