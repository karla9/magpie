package provider

// A workspace billed by usage — plan_type self_serve_business_usage_based,
// enterprise_cbp_usage_based — has no rate-limit windows at all: /wham/usage
// says "rate_limit": null, and the allowance the account spends is the
// individual limit its spend control counts. Read without it, the account
// had no window to show, so its card said it reported no allowance while
// 96% of it was left.

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// codexUsageSeat is /wham/usage as a usage-based workspace answers it,
// trimmed: the fields magpie reads, and nothing else.
func codexUsageSeat(extra map[string]any) map[string]any {
	body := map[string]any{
		"plan_type":  "self_serve_business_usage_based",
		"rate_limit": nil,
		"credits": map[string]any{
			"has_credits": true, "unlimited": false, "overage_limit_reached": false,
			"balance": nil,
		},
		"spend_control": map[string]any{
			"reached": false,
			"individual_limit": map[string]any{
				"source":              "workspace_spend_controls",
				"unit":                "credit",
				"limit":               "5100",
				"used":                "186.58448004722595",
				"remaining":           "4913.415519952774",
				"used_percent":        4,
				"remaining_percent":   96,
				"reset_after_seconds": 221528,
				"reset_at":            1793491200,
			},
		},
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

// readCodexUsageSeat is codexWindows against a server answering body.
func readCodexUsageSeat(t *testing.T, body map[string]any) (string, []QuotaWindow) {
	t.Helper()
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			t.Errorf("read %s, want /backend-api/wham/usage", r.URL.Path)
		}
		json.NewEncoder(w).Encode(body)
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	defer func() { CodexBase = old }()
	plan, windows, _, _, _, err := codexWindows(context.Background(), "tok", "acct-1")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return plan, windows
}

// The spend control's individual limit is the account's window: the share
// used, the count it is a share of, and the billing period's reset.
func TestCodexUsageBasedSeatShowsItsSpendWindow(t *testing.T) {
	plan, windows := readCodexUsageSeat(t, codexUsageSeat(nil))
	if plan != "self_serve_business_usage_based" {
		t.Errorf("plan %q", plan)
	}
	if len(windows) != 1 {
		t.Fatalf("windows %v, want the spend control's one", windows)
	}
	w := windows[0]
	if w.Name != "Credits" || w.Unit != "credits" {
		t.Errorf("window %q in %q, want Credits in credits", w.Name, w.Unit)
	}
	if w.Used != 4 {
		t.Errorf("used %v%%, want the spend control's 4", w.Used)
	}
	if math.Abs(w.Amount-186.58448004722595) > 1e-6 || w.Limit != 5100 {
		t.Errorf("amount %v of %v, want 186.58 of 5100", w.Amount, w.Limit)
	}
	if w.ResetsAt == nil || !w.ResetsAt.Equal(time.Unix(1793491200, 0)) {
		t.Errorf("resets %v, want the spend control's reset", w.ResetsAt)
	}
	// the credits the account holds are the cap itself, not something it
	// goes on past: nothing is left once the cap is reached
	if w.Credits {
		t.Error("the spend window says the account goes on past it on credits")
	}
}

// A spend control that names no share used is its own count of the cap.
func TestCodexSpendWindowReckonsItsShare(t *testing.T) {
	body := codexUsageSeat(nil)
	lim := body["spend_control"].(map[string]any)["individual_limit"].(map[string]any)
	delete(lim, "used_percent")
	_, windows := readCodexUsageSeat(t, body)
	if len(windows) != 1 {
		t.Fatalf("windows %v, want one", windows)
	}
	want := 186.58448004722595 / 5100 * 100
	if math.Abs(windows[0].Used-want) > 1e-9 {
		t.Errorf("used %v%%, want %v from the count and the cap", windows[0].Used, want)
	}
}

// A plan with windows of its own keeps them: its spend control is what it
// spends past them, not the allowance the card is about.
func TestCodexWindowsStayAheadOfTheSpendControl(t *testing.T) {
	body := codexUsageSeat(nil)
	body["plan_type"] = "plus"
	body["rate_limit"] = map[string]any{
		"primary_window":   map[string]any{"used_percent": 100, "limit_window_seconds": 18000},
		"secondary_window": map[string]any{"used_percent": 12, "limit_window_seconds": 604800},
	}
	_, windows := readCodexUsageSeat(t, body)
	if len(windows) != 2 {
		t.Fatalf("windows %v, want the plan's two and no spend window", windows)
	}
	for _, w := range windows {
		if w.Name == "Credits" {
			t.Errorf("the spend control was given as a window beside the plan's own: %v", w)
		}
	}
}

// A spend control with nothing to count reads as no window at all, as
// before: a card that tells no allowance is better than one making one up.
func TestCodexSpendWindowWithoutACap(t *testing.T) {
	body := codexUsageSeat(nil)
	body["spend_control"] = map[string]any{"reached": false}
	if _, windows := readCodexUsageSeat(t, body); len(windows) != 0 {
		t.Errorf("windows %v, want none", windows)
	}
	body["spend_control"] = map[string]any{"reached": false, "individual_limit": map[string]any{"unit": "credit", "limit": "0"}}
	if _, windows := readCodexUsageSeat(t, body); len(windows) != 0 {
		t.Errorf("windows %v, want none for a cap of zero", windows)
	}
}
