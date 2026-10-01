package kvstore

import (
	"strings"
	"testing"
)

func TestAPatternOfLiteralAndParameterSegmentsParses(t *testing.T) {
	t.Parallel()

	for _, written := range []string{"config", "requests/:userId", "session/:id", "rooms/:room/members/:member", "v1.cache/feature-flags/:flag_name"} {
		if _, err := ParsePattern(written); err != nil {
			t.Errorf("ParsePattern(%q) = %v, want it parsed", written, err)
		}
	}
}

func TestAMalformedPatternIsRefusedSayingWhatIsWrong(t *testing.T) {
	t.Parallel()

	for written, says := range map[string]string{
		"":                 "empty",
		"/session/:id":     "empty segment",
		"session/:id/":     "empty segment",
		"session//:id":     "empty segment",
		"session/{id}":     "hash tag",
		"user{1}/:id":      "hash tag",
		"session/:":        "parameter",
		"session/:1id":     "parameter",
		"session/:id-x":    "parameter",
		"session/:id/:id":  "twice",
		"session id/:id":   "literal",
		"session:id":       "literal",
		"sessions/a*b/:id": "literal",
	} {
		_, err := ParsePattern(written)
		if err == nil {
			t.Errorf("ParsePattern(%q) = nil, want it refused", written)
			continue
		}
		if !strings.Contains(err.Error(), says) {
			t.Errorf("ParsePattern(%q) = %q, want it to say %q", written, err, says)
		}
	}
}

func TestTwoPatternsOverlapWhenEverySegmentCouldNameTheSameKey(t *testing.T) {
	t.Parallel()

	for _, pair := range []struct {
		a, b    string
		overlap bool
	}{
		{"session/:id", "session/:other", true},
		{"session/:id", "session/current", true},
		{"a/:x", ":y/b", true},
		{"config", "config", true},
		{"session/:id", "sessions/:id", false},
		{"a/b", "a/c", false},
		{"a/:x", "a/:x/c", false},
		{"config", ":name/x", false},
	} {
		a, err := ParsePattern(pair.a)
		if err != nil {
			t.Fatal(err)
		}
		b, err := ParsePattern(pair.b)
		if err != nil {
			t.Fatal(err)
		}
		if got := a.Overlaps(b); got != pair.overlap {
			t.Errorf("%q overlaps %q = %v, want %v", pair.a, pair.b, got, pair.overlap)
		}
		if got := b.Overlaps(a); got != pair.overlap {
			t.Errorf("%q overlaps %q = %v, want %v whichever side asks", pair.b, pair.a, got, pair.overlap)
		}
	}
}
