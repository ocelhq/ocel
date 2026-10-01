package realtime

import (
	"strings"
	"testing"
)

func TestAPatternOfUpToFourLiteralOrParameterSegmentsParses(t *testing.T) {
	t.Parallel()

	for _, written := range []string{
		"status",
		"orders/:orderId",
		"rooms/:room_id",
		":roomId",
		"projects/:projectId/deploys/:deployId",
		"v2/feature-flags/:flag/Changes",
		"a/b/c/d",
	} {
		if _, err := ParsePattern(written); err != nil {
			t.Errorf("ParsePattern(%q) = %v, want it parsed", written, err)
		}
	}
}

func TestAMalformedPatternIsRefusedSayingWhatIsWrong(t *testing.T) {
	t.Parallel()

	for written, says := range map[string]string{
		"":                       "empty",
		"/orders/:orderId":       "empty segment",
		"orders/:orderId/":       "empty segment",
		"orders//:orderId":       "empty segment",
		"a/b/c/d/e":              "5 segments",
		"orders/:":               "parameter",
		"orders/:1st":            "parameter",
		"orders/:order-id":       "parameter",
		"rooms/:id/members/:id":  "twice",
		"order_items/:id":        "literal",
		"orders.v2/:id":          "literal",
		"-orders/:id":            "literal",
		"orders-/:id":            "literal",
		"orders/*":               "literal",
		"orders/{id}":            "literal",
		"café/:id":               "literal",
		strings.Repeat("a", 51):  "literal",
		"orders:id/:id":          "literal",
		"orders/:orderId/events": "",
	} {
		_, err := ParsePattern(written)
		if says == "" {
			if err != nil {
				t.Errorf("ParsePattern(%q) = %v, want it parsed", written, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("ParsePattern(%q) = nil, want it refused", written)
			continue
		}
		if !strings.Contains(err.Error(), says) {
			t.Errorf("ParsePattern(%q) = %q, want it to say %q", written, err, says)
		}
	}
}

func TestALiteralOfFiftyCharactersIsTheLongestASegmentHolds(t *testing.T) {
	t.Parallel()

	if _, err := ParsePattern(strings.Repeat("a", 50) + "/:id"); err != nil {
		t.Errorf("ParsePattern(50-character literal) = %v, want it parsed", err)
	}
}

func TestTwoPatternsOverlapWhenEverySegmentCouldNameTheSameChannel(t *testing.T) {
	t.Parallel()

	for _, pair := range []struct {
		a, b     string
		overlaps bool
	}{
		{"orders/:orderId", "orders/:id", true},
		{"orders/:orderId", "orders/latest", true},
		{"orders/:orderId", ":kind/o-1", true},
		{"status", "status", true},
		{"orders/:orderId", "rooms/:roomId", false},
		{"orders/:orderId", "orders/:orderId/events", false},
		{"orders/latest", "orders/first", false},
	} {
		a, b := mustParse(t, pair.a), mustParse(t, pair.b)
		if got := a.Overlaps(b); got != pair.overlaps {
			t.Errorf("%q overlaps %q = %v, want %v", pair.a, pair.b, got, pair.overlaps)
		}
		if got := b.Overlaps(a); got != pair.overlaps {
			t.Errorf("%q overlaps %q = %v, want %v", pair.b, pair.a, got, pair.overlaps)
		}
	}
}

func TestAPatternWithNoParameterSaysSo(t *testing.T) {
	t.Parallel()

	if mustParse(t, "status/live").HasParameters() {
		t.Error("status/live has parameters, want none")
	}
	if !mustParse(t, "orders/:orderId").HasParameters() {
		t.Error("orders/:orderId has no parameters, want one")
	}
}

func TestAWildcardSubscriptionCoversAnotherPatternWhoseChannelsShareItsPrefix(t *testing.T) {
	t.Parallel()

	for _, pair := range []struct {
		wildcard, other string
		covers          bool
	}{
		{"projects/:projectId/deploys/:deployId", "projects/:projectId/settings", true},
		{"projects/:projectId/deploys/:deployId", "projects/:projectId/deploys", true},
		{"projects/:projectId/deploys/:deployId", ":kind/:id/builds", true},
		{":roomId", "status", true},
		{":roomId", "status/live", true},
		{"rooms/:roomId", "status/live", false},
		{"projects/:projectId/deploys/:deployId", "projects", false},
		{"projects/:projectId/deploys/:deployId", "orders/:orderId", false},
		{"projects/:projectId/deploys/:deployId", "builds/:projectId/deploys/:deployId", false},
	} {
		wildcard, other := mustParse(t, pair.wildcard), mustParse(t, pair.other)
		if got := wildcard.WildcardCovers(other); got != pair.covers {
			t.Errorf("a wildcard subscription to %q covers %q = %v, want %v", pair.wildcard, pair.other, got, pair.covers)
		}
	}
}

func mustParse(t *testing.T, written string) Pattern {
	t.Helper()
	pattern, err := ParsePattern(written)
	if err != nil {
		t.Fatalf("ParsePattern(%q): %v", written, err)
	}
	return pattern
}
