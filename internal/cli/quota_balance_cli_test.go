package cli

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// magpie quota's % is what is used, so a balance amount asked as a
// percent (what was used of a plan) reads bare, never "left"; money, a
// count or "—" at the line's end keeps the " left" it always had.
func TestQuotaBalancePercentNotLeft(t *testing.T) {
	groupsHome(t)
	daily, used := 8.9, 90.0
	qs := []provider.Quota{
		{Provider: "relay", Name: "Relay", Kind: "balance", Balance: "Daily 8.9%",
			BalanceParts: []provider.BalancePart{{Label: "Daily", Text: "8.9%", Percent: &daily}}},
		{Provider: "mixed", Name: "Mixed", Kind: "balance", Balance: "Daily 8.9% · $70.00",
			BalanceParts: []provider.BalancePart{{Label: "Daily", Text: "8.9%", Percent: &daily}, {Text: "$70.00"}}},
		{Provider: "spent", Name: "Spent", Kind: "balance", Balance: "Balance $2.00 · Used 90%",
			BalanceParts: []provider.BalancePart{{Label: "Balance", Text: "$2.00"}, {Label: "Used", Text: "90%", Percent: &used}}},
		{Provider: "gone", Name: "Gone", Kind: "balance", Balance: "Daily 8.9% · Month —",
			BalanceParts: []provider.BalancePart{{Label: "Daily", Text: "8.9%", Percent: &daily}, {Label: "Month", Text: "—", Error: "Month: no such window"}}},
		{Provider: "deepseek", Name: "DeepSeek", Kind: "balance", Balance: "¥12.30"},
	}
	out, _ := stdoutOf(t, func() error { printQuotas(qs); return nil })
	line := func(name string) string { // by quotaTitle: the provider id
		for l := range strings.SplitSeq(out, "\n") {
			if strings.HasPrefix(l, name+" ") {
				return strings.Join(strings.Fields(l), " ")
			}
		}
		t.Fatalf("no line for %s in\n%s", name, out)
		return ""
	}
	for name, want := range map[string]string{
		"relay":    "relay balance Daily 8.9%",
		"mixed":    "mixed balance Daily 8.9% · $70.00 left",
		"spent":    "spent balance Balance $2.00 · Used 90%",
		"gone":     "gone balance Daily 8.9% · Month — left",
		"deepseek": "deepseek balance ¥12.30 left",
	} {
		if got := line(name); !strings.HasPrefix(got, want) || strings.HasPrefix(got, want+" left") {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	if strings.Contains(out, "% left") {
		t.Errorf("a used share called left:\n%s", out)
	}
}
