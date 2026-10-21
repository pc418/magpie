package routes

import (
	"slices"
	"testing"
	"time"
)

func seats(n int) []Seat {
	out := make([]Seat, n)
	for i := range out {
		out[i] = Seat{Key: string(rune('a' + i))}
	}
	return out
}

// Registering stacks: the last one serves until it is removed, and the one
// under it serves again; removing twice removes once. Smart is served with
// nothing registered, and a routing with none registered is not.
func TestRegisterStacks(t *testing.T) {
	if s, ok := Find(""); !ok || s.Order == nil {
		t.Fatal("Smart isn't served")
	}
	if Served("t-stack") {
		t.Fatal("served with none registered")
	}
	first := Register("t-stack", Strategy{Order: func(string, []Seat) []int { return []int{1, 0} }})
	second := Register("t-stack", Strategy{Order: func(string, []Seat) []int { return []int{0, 1} }, Exact: true})
	if s, _ := Find("t-stack"); !s.Exact {
		t.Fatal("the last registered doesn't serve")
	}
	second()
	second()
	if s, ok := Find("t-stack"); !ok || s.Exact {
		t.Fatalf("after removing the last, the first doesn't serve: %v", ok)
	}
	first()
	if Served("t-stack") {
		t.Fatal("still served after both were removed")
	}
}

// A strategy that panics, or answers what isn't a permutation of its
// seats, leaves them in their order: every route is still tried.
func TestApplyFailsOpen(t *testing.T) {
	for name, order := range map[string]func(string, []Seat) []int{
		"panics":    func(string, []Seat) []int { panic("boom") },
		"short":     func(string, []Seat) []int { return []int{2, 1} },
		"repeats":   func(string, []Seat) []int { return []int{2, 2, 0} },
		"out of it": func(string, []Seat) []int { return []int{3, 1, 0} },
		"negative":  func(string, []Seat) []int { return []int{-1, 1, 0} },
		"nil":       func(string, []Seat) []int { return nil },
	} {
		if got := Apply(Strategy{Order: order}, "p", seats(3)); !slices.Equal(got, []int{0, 1, 2}) {
			t.Errorf("%s: %v, want the listed order", name, got)
		}
	}
	if got := Apply(Strategy{Order: func(string, []Seat) []int { return []int{2, 0, 1} }}, "p", seats(3)); !slices.Equal(got, []int{2, 0, 1}) {
		t.Errorf("a good order was changed: %v", got)
	}
}

// Smart: those with room by when they renew, soonest first, unknown last;
// then those running low, then those all but used up, each by use. One
// learning goes first.
func TestSmart(t *testing.T) {
	now := time.Now()
	s := []Seat{
		{Key: "spent", Used: 99},
		{Key: "later", Used: 10, Soon: []time.Time{now.Add(48 * time.Hour)}},
		{Key: "low", Used: 92},
		{Key: "unknown"},
		{Key: "sooner", Used: 50, Soon: []time.Time{now.Add(2 * time.Hour)}},
		{Key: "learns", Learns: true},
	}
	got := keys(s, Apply(Smart, "p", s))
	if want := []string{"learns", "sooner", "later", "unknown", "low", "spent"}; !slices.Equal(got, want) {
		t.Fatalf("Smart: %v, want %v", got, want)
	}
}

// Smart passes over a biggest window that can't be used up before it
// renews any more (Seat.Unreached) and ranks the seat by the next: an
// Agent Plan whose month renews in 15 days, its week tomorrow, goes before
// a week renewing in three days once its month is out of reach (#5639),
// and after it while the month isn't. An Unreached past Soon is harmless.
func TestSmartPassesAnUnreachedWindow(t *testing.T) {
	now := time.Now()
	week := now.Add(3 * 24 * time.Hour)
	plan := []time.Time{now.Add(15 * 24 * time.Hour), now.Add(24 * time.Hour), now.Add(5 * time.Hour)}
	for _, c := range []struct {
		unreached int
		want      []string
	}{{0, []string{"weekly", "plan"}}, {1, []string{"plan", "weekly"}}, {5, []string{"weekly", "plan"}}} {
		s := []Seat{
			{Key: "weekly", Used: 10, Soon: []time.Time{week, now.Add(5 * time.Hour)}},
			{Key: "plan", Used: 8, Soon: plan, Unreached: c.unreached},
		}
		if got := keys(s, Apply(Smart, "p", s)); !slices.Equal(got, c.want) {
			t.Errorf("unreached %d: %v, want %v", c.unreached, got, c.want)
		}
	}
}

func keys(s []Seat, order []int) []string {
	out := make([]string, len(order))
	for i, j := range order {
		out[i] = s[j].Key
	}
	return out
}

// In turn starts one further along each time, per pool.
func TestRotate(t *testing.T) {
	Forget("r1", "r2")
	s := seats(3)
	var got [][]string
	for range 4 {
		got = append(got, keys(s, Apply(Rotate, "r1", s)))
	}
	want := [][]string{{"a", "b", "c"}, {"b", "c", "a"}, {"c", "a", "b"}, {"a", "b", "c"}}
	for i := range want {
		if !slices.Equal(got[i], want[i]) {
			t.Fatalf("turn %d: %v, want %v", i, got[i], want[i])
		}
	}
	if g := keys(s, Apply(Rotate, "r2", s)); g[0] != "a" {
		t.Fatalf("another pool shares the turn: %v", g)
	}
	Forget("r1")
	if g := keys(s, Apply(Rotate, "r1", s)); g[0] != "a" {
		t.Fatalf("Forget kept the turn: %v", g)
	}
}

// Least used first by share used, then by tokens; in order is the listed
// order, and exact.
func TestLeastUsedAndOrdered(t *testing.T) {
	s := []Seat{{Key: "a", Used: 50}, {Key: "b", Used: 10, Tokens: 9}, {Key: "c", Used: 10, Tokens: 1}}
	if got := keys(s, Apply(LeastUsed, "p", s)); !slices.Equal(got, []string{"c", "b", "a"}) {
		t.Fatalf("least used: %v", got)
	}
	if got := keys(s, Apply(Ordered, "p", s)); !slices.Equal(got, []string{"a", "b", "c"}) || !Ordered.Exact {
		t.Fatalf("in order: %v", got)
	}
}

// A seat that tells no share of its allowance — a pay-as-you-go key, a
// prepaid wallet, an account whose use wasn't read — has Used read 0, and
// by that 0 alone it went first, burning paid balance while subscription
// quota sat idle (#4535). It goes after those whose share was told and has
// room, and before those running low or spent, as Smart and Pace keep the
// unknown; a learning account still goes first, once. Of the shareless, a
// wallet goes by what it has left and a key by what magpie sent it lately.
func TestLeastUsedSeatsWithNoShare(t *testing.T) {
	now := time.Now()
	sub := func(key string, used float64) Seat {
		return Seat{Key: key, Known: true, Used: used, Soon: []time.Time{now.Add(48 * time.Hour)}}
	}
	key := Seat{Key: "key"} // a pay-as-you-go key: no window, so Used reads 0
	s := []Seat{key, sub("sub", 5)}
	if got := keys(s, Apply(LeastUsed, "p", s)); !slices.Equal(got, []string{"sub", "key"}) {
		t.Fatalf("a key with no share went first: %v", got)
	}
	// a wallet has no share either, but what it has left still orders it
	// behind a told share, and before a key that told nothing at all
	wallet := Seat{Key: "wallet", Known: true, Left: 50, LeftIn: "prov\x00usd"}
	s = []Seat{key, wallet, sub("sub", 5)}
	if got := keys(s, Apply(LeastUsed, "p", s)); !slices.Equal(got, []string{"sub", "wallet", "key"}) {
		t.Fatalf("wallet: %v, want [sub wallet key]", got)
	}
	// one running low or spent is kept for when the others can't, past a
	// key: its share is nearly gone, but it isn't gone
	s = []Seat{key, sub("fine", 5), sub("low", 92), sub("spent", 99)}
	if got := keys(s, Apply(LeastUsed, "p", s)); !slices.Equal(got, []string{"fine", "key", "low", "spent"}) {
		t.Fatalf("low and spent: %v, want [fine key low spent]", got)
	}
	// one learning goes first, once, as in Smart and Pace
	learns := Seat{Key: "learns", Learns: true}
	s = []Seat{key, learns, sub("sub", 5)}
	if got := keys(s, Apply(LeastUsed, "p", s)); !slices.Equal(got, []string{"learns", "sub", "key"}) {
		t.Fatalf("learns: %v, want [learns sub key]", got)
	}
	// those with a told share go by it as before
	s = []Seat{sub("much", 80), sub("little", 10)}
	if got := keys(s, Apply(LeastUsed, "p", s)); !slices.Equal(got, []string{"little", "much"}) {
		t.Fatalf("told shares: %v, want [little much]", got)
	}
}

// Use before expiry (pace): the most of a week left per hour first, one running low
// by its pace like the rest; one all but used up last, whatever its pace;
// equal paces, and the tokens sent lately, leave the listed order.
//
// PIN (2026-10-20, owner): pace is the one rule, spent last; the review's
// 90% tier, learns-first, 10% bands and tokens tie-break are gone
// (docs/261020-eval-WEEKLY_PACE_SIMPLIFY-routes_pace.md §6).
func TestPace(t *testing.T) {
	s := []Seat{{Key: "slow", Pace: 1}, {Key: "low", Used: 95, Pace: 50}, {Key: "fast", Pace: 10}, {Key: "spent", Used: 98, Pace: 90}}
	if got := keys(s, Apply(Pace, "p", s)); !slices.Equal(got, []string{"low", "fast", "slow", "spent"}) {
		t.Fatalf("pace: %v", got)
	}
	// just under spent counts by its pace; two spent go by theirs
	s = []Seat{{Key: "a", Pace: 1}, {Key: "under", Used: 97.9, Pace: 2}, {Key: "s1", Used: 100, Pace: 1}, {Key: "s2", Used: 98, Pace: 3}}
	if got := keys(s, Apply(Pace, "p", s)); !slices.Equal(got, []string{"under", "a", "s2", "s1"}) {
		t.Fatalf("spent boundary: %v", got)
	}
	// exact ties keep their order, whatever was sent to them lately
	s = []Seat{{Key: "x", Pace: 0.5, Tokens: 900}, {Key: "y", Pace: 0.5, Tokens: 1}, {Key: "z", Pace: 0.5, Tokens: 50}}
	if got := keys(s, Apply(Pace, "p", s)); !slices.Equal(got, []string{"x", "y", "z"}) {
		t.Fatalf("ties: %v", got)
	}
	// a pace a twentieth below the top is behind it, not alike
	s = []Seat{{Key: "near", Pace: 0.95, Tokens: 0}, {Key: "top", Pace: 1, Tokens: 900}}
	if got := keys(s, Apply(Pace, "p", s)); !slices.Equal(got, []string{"top", "near"}) {
		t.Fatalf("no band: %v", got)
	}
	// of the spent, one going on on its credits first; one set to spend
	// its credits before another account isn't spent, and goes by its pace
	s = []Seat{{Key: "s", Used: 100, Pace: 5}, {Key: "credits", Used: 100, Credits: true}, {Key: "first", Used: 100, Credits: true, First: true}, {Key: "a", Pace: 1}}
	if got := keys(s, Apply(Pace, "p", s)); !slices.Equal(got, []string{"a", "first", "credits", "s"}) {
		t.Fatalf("credits: %v, want [a first credits s]", got)
	}
	// wallets have no week: of equal paces, the one with more left first
	s = []Seat{{Key: "w1", Known: true, Left: 5, LeftIn: "p\x00usd"}, {Key: "w2", Known: true, Left: 50, LeftIn: "p\x00usd"}, {Key: "sub", Pace: 0.2}}
	if got := keys(s, Apply(Pace, "p", s)); !slices.Equal(got, []string{"sub", "w2", "w1"}) {
		t.Fatalf("wallets: %v, want [sub w2 w1]", got)
	}
	// every listing of three paces gives the one order, and paces that
	// differ in their last bits still order strictly: no epsilon makes
	// them alike
	for _, listed := range [][]float64{{1, 2, 3}, {3, 1, 2}, {2, 3, 1}, {1, 3, 2}, {3, 2, 1}} {
		s = make([]Seat, len(listed))
		for i, p := range listed {
			s[i] = Seat{Key: string(rune('0' + p)), Pace: p}
		}
		if got := keys(s, Apply(Pace, "p", s)); !slices.Equal(got, []string{"3", "2", "1"}) {
			t.Fatalf("paces listed %v: %v", listed, got)
		}
	}
	s = []Seat{{Key: "1.0", Pace: 1.0}, {Key: "1.0000001", Pace: 1.0000001}}
	if got := keys(s, Apply(Pace, "p", s)); !slices.Equal(got, []string{"1.0000001", "1.0"}) {
		t.Fatalf("adjacent paces: %v", got)
	}
	if Pace.ByUse {
		t.Fatal("pace weighs no tokens, so the trace shouldn't show them")
	}
}

// A seat at 90% of its week alone (WeekLow) keeps its place in Smart and
// Use before expiry: renewing sooner, or with more per hour to lose, it
// goes ahead of one with room (ysicing_01858 on Discord). Least used first
// still puts it by its share; at 98% it is all but used up in every one.
func TestWeekLowKeepsItsPlace(t *testing.T) {
	now := time.Now()
	s := []Seat{
		{Key: "room", Known: true, Used: 30, Soon: []time.Time{now.Add(48 * time.Hour)}, Pace: 70.0 / 48},
		{Key: "week", Known: true, Used: 91, WeekLow: true, Soon: []time.Time{now.Add(3 * time.Hour)}, Pace: 3},
	}
	for name, st := range map[string]Strategy{"smart": Smart, "pace": Pace} {
		if got := keys(s, Apply(st, "p", s)); !slices.Equal(got, []string{"week", "room"}) {
			t.Errorf("%s: %v, want [week room]", name, got)
		}
	}
	if got := keys(s, Apply(LeastUsed, "p", s)); !slices.Equal(got, []string{"room", "week"}) {
		t.Errorf("least used: %v, want [room week]", got)
	}
	s[1].Used = 98
	for name, st := range map[string]Strategy{"smart": Smart, "pace": Pace} {
		if got := keys(s, Apply(st, "p", s)); !slices.Equal(got, []string{"room", "week"}) {
			t.Errorf("%s at 98%%: %v, want [room week]", name, got)
		}
	}
}

// By weight: over six requests a seat weighing 2 goes first twice as often
// as each weighing 1, and the others follow the one picked in their order.
func TestWeighted(t *testing.T) {
	Forget("w")
	s := []Seat{{Key: "a", Weight: 2}, {Key: "b", Weight: 1}, {Key: "c", Weight: 1}}
	firsts := map[string]int{}
	for range 8 {
		order := keys(s, Apply(Weighted, "w", s))
		firsts[order[0]]++
		rest := slices.DeleteFunc(slices.Clone([]string{"a", "b", "c"}), func(k string) bool { return k == order[0] })
		if !slices.Equal(order[1:], rest) {
			t.Fatalf("the others aren't in their order: %v", order)
		}
	}
	if firsts["a"] != 4 || firsts["b"] != 2 || firsts["c"] != 2 {
		t.Fatalf("by weight: %v, want a 4, b 2, c 2", firsts)
	}
}

// Of those all but used up, one that goes on on its credits (a Codex
// account holding them) goes before those with nothing left, though it
// has used more, in Smart, Use before expiry and Least used alike; still after
// every one with room or running low, its allowance spent before its
// credits are.
func TestCreditsFirstOfTheSpent(t *testing.T) {
	s := []Seat{
		{Key: "spent", Used: 98},
		{Key: "credits", Used: 100, Credits: true},
		{Key: "low", Used: 92, Credits: true},
		{Key: "fine", Used: 20},
	}
	// not Weekly pace: it has no running-low tier (PIN 2026-10-20, owner;
	// TestPace holds its credits case)
	for name, st := range map[string]Strategy{"smart": Smart, "least used": LeastUsed} {
		if got := keys(s, Apply(st, "p", s)); !slices.Equal(got, []string{"fine", "low", "credits", "spent"}) {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// A seat whose provider is set to be used first (Seat.Lead) goes ahead of
// the others Smart weighs it with: an API key's monthly credit before a
// subscription with room, which renews soonest (hzh_1119 on Discord). One
// all but used up goes with the others that are, and with none set to
// lead, Smart goes as it did.
func TestSmartLeadFirst(t *testing.T) {
	now := time.Now()
	s := []Seat{
		{Key: "plus", Used: 10, Soon: []time.Time{now.Add(2 * time.Hour)}},
		{Key: "pro", Used: 40, Soon: []time.Time{now.Add(24 * time.Hour)}},
		{Key: "api", Lead: true},
		{Key: "api-spent", Lead: true, Used: 99},
		{Key: "spent", Used: 98},
	}
	got := keys(s, Apply(Smart, "p", s))
	if want := []string{"api", "plus", "pro", "spent", "api-spent"}; !slices.Equal(got, want) {
		t.Fatalf("Smart with a lead: %v, want %v", got, want)
	}
	for i := range s {
		s[i].Lead = false
	}
	got = keys(s, Apply(Smart, "p", s))
	if want := []string{"plus", "pro", "api", "spent", "api-spent"}; !slices.Equal(got, want) {
		t.Fatalf("Smart with none set: %v, want %v", got, want)
	}
}
