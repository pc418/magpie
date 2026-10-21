package provider

import (
	"math"
	"testing"
	"time"
)

// A share used that isn't a number is a failed read of that window, not
// a reading: NaN compares as equal to every share, so sorted by it an
// order of accounts isn't one (1, NaN, 2). Kimi Code's /usages numbers
// come as strings, and strconv takes "NaN" and "Infinity"; allowanceOf,
// which every reader's windows go through to routing, leaves such a
// window out and keeps the others, and an account with no other window
// is not known.
func TestUnreadShareLeftOut(t *testing.T) {
	_, ws, err := readKimiCode([]byte(`{"usage":{"limit":"100","used":"NaN","resetTime":"2026-09-30T05:24:18.443553353Z"},
		"limits":[{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},"detail":{"limit":"50","remaining":"40","resetTime":"2026-09-27T09:00:00Z"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 1 || ws[0].Name != "5 hours" || ws[0].Used != 20 {
		t.Fatalf(`"used":"NaN" read as a window: %+v`, ws)
	}
	for _, body := range []string{
		`{"usage":{"limit":"Infinity","used":"12"}}`,
		`{"usage":{"limit":"100","remaining":"-Infinity"}}`,
		`{"usage":{"limit":"100","used":"+Inf"}}`,
	} {
		if _, ws, _ := readKimiCode([]byte(body)); len(ws) != 0 {
			t.Fatalf("%s read as %+v", body, ws)
		}
	}

	now := time.Now()
	week := 7 * 24 * time.Hour
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		in := []QuotaWindow{{Name: "5 hours", Span: 5 * time.Hour, Used: bad}, {Name: "7 days", Span: week, Used: 40}}
		a := allowanceOf(in, now)
		if len(a) != 1 || a[0].Name != "7 days" || a[0].Used != 40 {
			t.Fatalf("Used %v: %+v", bad, a)
		}
		if used, _ := a.For("m", now); used != 40 {
			t.Fatalf("Used %v: the share is %v, want the week's 40", bad, used)
		}
		if p, _ := a.Pace("m", now); math.IsNaN(p) || math.IsInf(p, 0) {
			t.Fatalf("Used %v: pace %v", bad, p)
		}
		if !readWindows(in) {
			t.Fatalf("Used %v: the week is read", bad)
		}
		if only := in[:1]; readWindows(only) || len(allowanceOf(only, now)) != 0 {
			t.Fatalf("Used %v alone: still a reading", bad)
		}
	}
	// a window set aside still counts as read, as before: an account of
	// unlimited windows alone is known, with nothing that stops it
	if !readWindows([]QuotaWindow{{Name: "Unlimited", Unlimited: true, Aside: true}}) {
		t.Fatal("an aside window counted as unread")
	}
}
