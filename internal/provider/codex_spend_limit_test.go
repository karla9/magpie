package provider

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestCodexSpendingLimitWithoutRateWindows(t *testing.T) {
	fixture, err := os.ReadFile("testdata/codex_usage_enterprise_spend_limit.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		used string
		want float64
		held bool
	}{
		{"unused budget", "0", 0, false},
		{"budget remains", "250.5", 25.05, false},
		{"just below cap", "899.99", 89.999, false},
		{"at cap", "900", 90, true},
		{"spent despite credits", "1000", 100, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal(fixture, &body); err != nil {
				t.Fatal(err)
			}
			limit := body["spend_control"].(map[string]any)["individual_limit"].(map[string]any)
			used, err := strconv.ParseFloat(test.used, 64)
			if err != nil {
				t.Fatal(err)
			}
			limit["used"] = test.used
			limit["remaining"] = strconv.FormatFloat(1000-used, 'f', -1, 64)
			limit["used_percent"] = math.Round(test.want)
			limit["remaining_percent"] = 100 - math.Round(test.want)
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/backend-api/wham/usage" {
					t.Errorf("unexpected usage path %q", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				json.NewEncoder(w).Encode(body)
			}))
			defer fake.Close()
			old := CodexBase
			CodexBase = fake.URL + "/backend-api/codex"
			defer func() { CodexBase = old }()
			plan, windows, _, _, _, err := codexWindows(context.Background(), "tok", "acct-1")
			if err != nil || plan != "ent26" || len(windows) != 1 {
				t.Fatalf("plan %q, windows %v, err %v; want the Enterprise spending limit", plan, windows, err)
			}
			window := windows[0]
			if math.Abs(window.Used-test.want) > 0.000001 || window.Amount != used || window.Limit != 1000 || window.Unit != "USD" {
				t.Errorf("spending limit %+v, want used %g of 1000 USD", window, test.want)
			}
			if window.ResetsAt == nil || window.ResetsAt.Unix() != 1796083200 || window.ResetSecs != 2592000 {
				t.Errorf("budget reset lost: %+v", window)
			}
			if window.Credits || window.Aside || window.Text {
				t.Errorf("spending limit is not a hard quota: %+v", window)
			}
			now := time.Unix(1793491200, 0)
			reading := readNow(SubscriptionQuota{Provider: "codex", Windows: windows})
			allowance, known := routedAllowance("codex", "me@example.com", reading, now)
			if reading.ReadAt == nil || !known {
				t.Fatal("spending-only account still has no fresh, routable allowance")
			}
			if held := allowance.CapHeld("gpt-6.1-sol", WindowCaps{All: 90}, now); (held != nil) != test.held {
				t.Errorf("90%% cap hold %+v, want held %v", held, test.held)
			}
			if allowance.OnCredits() {
				t.Fatal("credits bypass the spending limit")
			}
		})
	}
}

func TestCodexSpendingLimitKeepsUnknownAndOrdinaryWindows(t *testing.T) {
	for _, test := range []struct {
		name     string
		budget   any
		rate     any
		wantSize int
	}{
		{"no budget", nil, nil, 0},
		{"no amount", map[string]any{"limit": "1000"}, nil, 0},
		{"null amount", map[string]any{"limit": "1000", "used": nil}, nil, 0},
		{"bad amount", map[string]any{"limit": "1000", "used": "NaN"}, nil, 0},
		{"negative amount", map[string]any{"limit": "1000", "used": "-1"}, nil, 0},
		{"bad total", map[string]any{"limit": "Inf", "used": "1"}, nil, 0},
		{"zero total", map[string]any{"limit": "0", "used": "0"}, nil, 0},
		{"negative total", map[string]any{"limit": "-1", "used": "0"}, nil, 0},
		{"ordinary window", map[string]any{"limit": "1000", "used": "950"}, map[string]any{
			"primary_window": map[string]any{"used_percent": 12, "limit_window_seconds": 18000},
		}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{
					"plan_type": "ent26", "rate_limit": test.rate,
					"spend_control": map[string]any{"reached": false, "individual_limit": test.budget},
				})
			}))
			defer fake.Close()
			old := CodexBase
			CodexBase = fake.URL + "/backend-api/codex"
			defer func() { CodexBase = old }()
			_, windows, _, _, _, err := codexWindows(context.Background(), "tok", "acct-1")
			if err != nil || len(windows) != test.wantSize {
				t.Fatalf("windows %v, err %v; want %d windows", windows, err, test.wantSize)
			}
			if len(windows) > 0 && (windows[0].Used != 12 || windows[0].Span != 5*time.Hour) {
				t.Errorf("ordinary allowance changed: %+v", windows[0])
			}
		})
	}
}
