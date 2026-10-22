package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

// A balance field with several amounts, or a percent, comes to the card
// each amount apart, its label apart from its figure and a percent's
// share for a meter (#420); one amount alone is the figure as it was.
func TestBalanceParts(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}
	keyBalanceCache.data = nil
	t.Cleanup(func() { keyBalanceCache.data = nil })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"total_available":1000000,"today":2.5,"used":45,"granted":50}}`))
	}))
	defer srv.Close()
	for _, p := range []Provider{
		{ID: "several", Name: "Several", Key: "sk-1", BalanceURL: srv.URL + "/b", BalancePath: "Balance: $data.total_available / 500000; Today: $data.today; Used: data.used / data.granted %"},
		{ID: "share", Name: "Share", Key: "sk-2", BalanceURL: srv.URL + "/b", BalancePath: "data.used / data.granted %"},
		{ID: "one", Name: "One", Key: "sk-3", BalanceURL: srv.URL + "/b", BalancePath: "$data.total_available / 500000"},
	} {
		p.Chat = srv.URL + "/v1"
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]SubscriptionQuota{}
	for _, q := range KeyBalances(context.Background()) {
		got[q.Provider] = q
	}
	parts := func(id string) string {
		b, _ := json.Marshal(got[id].BalanceParts)
		return string(b)
	}
	if q := got["several"]; q.Balance != "Balance $2.00 · Today $2.50 · Used 90%" {
		t.Errorf("several: the line as it was, got %q", q.Balance)
	}
	if want := `[{"label":"Balance","text":"$2.00"},{"label":"Today","text":"$2.50"},{"label":"Used","text":"90%","percent":90}]`; parts("several") != want {
		t.Errorf("several: %s\nwant %s", parts("several"), want)
	}
	if want := `[{"text":"90%","percent":90}]`; parts("share") != want {
		t.Errorf("share: %s\nwant %s", parts("share"), want)
	}
	if q := got["one"]; q.Balance != "$2.00" || q.BalanceParts != nil {
		t.Errorf("one: %q %s, want the figure alone", q.Balance, parts("one"))
	}
	// when each was read, for the card to say, which the minute the
	// balances are kept for leaves as it was
	for id, q := range got {
		if q.ReadAt == nil || time.Since(*q.ReadAt) > time.Minute {
			t.Errorf("%s: read at %v", id, q.ReadAt)
		}
	}
	first := *got["one"].ReadAt
	for _, q := range KeyBalances(context.Background()) {
		if q.Provider == "one" && !q.ReadAt.Equal(first) {
			t.Errorf("one: kept, it says read at %v, not %v", q.ReadAt, first)
		}
	}
}

// A balance amount asked as a percent is what was used of a plan, so
// where what is left is shown it turns as a window does: the reply from
// the issue (windows.0.used 8.9, "Daily: windows.0.used / 100 %") reads
// "Daily 91.1%" left; money, "—" and a balance with no percent stay as
// they are, and nothing changes where what is used is shown.
func TestBalanceTextLeft(t *testing.T) {
	reply := []byte(`{"windows":[{"name":"daily","used":8.9},{"name":"weekly","used":100}],"credits":70}`)
	parts, err := readBalanceParts(reply, "Daily: windows.0.used / 100 %; Week: windows.1.used / 100 %; $credits; Month: windows.5.used / 100 %")
	if err != nil {
		t.Fatal(err)
	}
	q := SubscriptionQuota{Balance: joinBalanceParts(parts), BalanceParts: cardParts(parts)}
	if q.Balance != "Daily 8.9% · Week 100% · $70.00 · Month —" {
		t.Fatalf("balance %q", q.Balance)
	}
	if got := BalanceText(q, false, "left"); got != q.Balance {
		t.Errorf("used: %q, want %q", got, q.Balance)
	}
	if got, want := BalanceText(q, true, ""), "Daily 91.1% · Week 0% · $70.00 · Month —"; got != want {
		t.Errorf("left: %q, want %q", got, want)
	}
	if got, want := BalanceText(q, true, "left"), "Daily 91.1% left · Week 0% left · $70.00 · Month —"; got != want {
		t.Errorf("left, said: %q, want %q", got, want)
	}
	if q.BalanceParts[0].Text != "8.9%" {
		t.Errorf("the card's own parts changed: %+v", q.BalanceParts[0])
	}
	// one amount alone, money, has no parts: as it is
	money := SubscriptionQuota{Balance: "$2.00"}
	if got := BalanceText(money, true, "left"); got != "$2.00" {
		t.Errorf("money: %q", got)
	}
}

// A line says " left" after a balance's money, a count or "—", never
// after an amount asked as a percent, which is what was used: that one
// reads bare with Used on and "{n}% left" with Left on.
func TestBalanceLine(t *testing.T) {
	daily, used := 8.9, 90.0
	pct := BalancePart{Label: "Daily", Text: "8.9%", Percent: &daily}
	for _, c := range []struct {
		name, balance string
		parts         []BalancePart
		left          bool
		want          string
	}{
		{"money", "¥12.30", nil, false, "¥12.30 left"},
		{"money, left on", "¥12.30", nil, true, "¥12.30 left"},
		{"several money", "Balance $2.00 · Today $2.50", []BalancePart{{Label: "Balance", Text: "$2.00"}, {Label: "Today", Text: "$2.50"}}, false, "Balance $2.00 · Today $2.50 left"},
		{"percent alone", "Daily 8.9%", []BalancePart{pct}, false, "Daily 8.9%"},
		{"percent alone, left on", "Daily 8.9%", []BalancePart{pct}, true, "Daily 91.1% left"},
		{"percent then money", "Daily 8.9% · $70.00", []BalancePart{pct, {Text: "$70.00"}}, false, "Daily 8.9% · $70.00 left"},
		{"percent then money, left on", "Daily 8.9% · $70.00", []BalancePart{pct, {Text: "$70.00"}}, true, "Daily 91.1% left · $70.00 left"},
		{"money then percent", "Balance $2.00 · Used 90%", []BalancePart{{Label: "Balance", Text: "$2.00"}, {Label: "Used", Text: "90%", Percent: &used}}, false, "Balance $2.00 · Used 90%"},
		{"money then percent, left on", "Balance $2.00 · Used 90%", []BalancePart{{Label: "Balance", Text: "$2.00"}, {Label: "Used", Text: "90%", Percent: &used}}, true, "Balance $2.00 · Used 10% left"},
		{"percent then —", "Daily 8.9% · Month —", []BalancePart{pct, {Label: "Month", Text: "—", Error: "Month: none"}}, false, "Daily 8.9% · Month — left"},
	} {
		figure, word := BalanceLine(c.balance, c.parts, c.left)
		if got := figure + word; got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// magpie quota's card carries the parts, and --json and the gateway
	// tell the balance as they did, without them
	r := quotaReport(nil, nil, []SubscriptionQuota{{Provider: "relay", Name: "Relay", Balance: "Daily 8.9%", BalanceParts: []BalancePart{pct}}}, time.Now())
	if len(r) != 1 || len(r[0].BalanceParts) != 1 || r[0].BalanceParts[0].Percent == nil {
		t.Fatalf("parts not carried: %+v", r)
	}
	b, err := json.Marshal(r[0])
	if err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal(b, &sent); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{"provider": "relay", "name": "Relay", "kind": "balance", "balance": "Daily 8.9%"} {
		if sent[k] != want {
			t.Errorf("sent %s = %v, want %v: %s", k, sent[k], want, b)
		}
	}
	if _, ok := sent["balanceParts"]; ok || strings.Contains(string(b), "percent") {
		t.Errorf("parts sent: %s", b)
	}
}

// The editor's example of "%", credits.used / 70 %, is the share used: 63
// of 70 used reads 90% with Used on and "10% left" with Left on.
func TestBalancePercentExampleIsUsed(t *testing.T) {
	parts, err := readBalanceParts([]byte(`{"credits":{"used":63}}`), "credits.used / 70 %")
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].Text != "90%" || parts[0].Percent == nil || *parts[0].Percent != 90 {
		t.Fatalf("parts %+v", parts)
	}
	balance := joinBalanceParts(parts)
	for _, c := range []struct {
		left bool
		want string
	}{{false, "90%"}, {true, "10% left"}} {
		if figure, word := BalanceLine(balance, parts, c.left); figure+word != c.want {
			t.Errorf("left %v: %q, want %q", c.left, figure+word, c.want)
		}
	}
}
