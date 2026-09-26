package provider

import (
	"strings"
	"testing"
)

func TestWrittenByVersion(t *testing.T) {
	t.Parallel()

	t.Run("a release version parses", func(t *testing.T) {
		t.Parallel()

		for _, raw := range []string{"1.2.3", "v1.2.3", "0.0.1", "1.2.3-rc.1", "1.2.3+meta"} {
			if w := WrittenByVersion(raw); !w.Release() {
				t.Errorf("WrittenByVersion(%q).Release() = false, want true", raw)
			}
		}
	})

	t.Run("a dev build never parses as a version", func(t *testing.T) {
		t.Parallel()

		for _, raw := range []string{"", "dev", "(devel)", "dev+cafe", "1.2", "1.2.3.4", "nightly"} {
			if w := WrittenBy(raw); w.Release() {
				t.Errorf("Writer(%q).Release() = true, want false", raw)
			}
		}
	})

	t.Run("a dev build stamps its revision", func(t *testing.T) {
		t.Parallel()

		w := writerFor("dev", "cafebabe")
		if string(w) != "dev+cafebabe" {
			t.Fatalf("writerFor(dev, cafebabe) = %q, want dev+cafebabe", w)
		}
		if w.Release() {
			t.Fatalf("%q must never read as a release", w)
		}
	})

	t.Run("a dev build without a revision is still dev", func(t *testing.T) {
		t.Parallel()

		if w := writerFor("dev", ""); string(w) != "dev" {
			t.Fatalf("writerFor(dev, \"\") = %q, want dev", w)
		}
	})

	t.Run("an unset writer reads as unknown", func(t *testing.T) {
		t.Parallel()

		if got := WrittenBy("").String(); got != "unknown" {
			t.Fatalf("Writer(\"\").String() = %q, want unknown", got)
		}
	})

	t.Run("the live writer is never empty", func(t *testing.T) {
		t.Parallel()

		if got := WrittenByVersion("dev"); !strings.HasPrefix(string(got), "dev") {
			t.Fatalf("WrittenByVersion(dev) = %q, want it to start with dev", got)
		}
	})
}

func TestWrittenByDevelopment(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		writer WrittenBy
		want   bool
	}{
		{"dev", true},
		{"dev+cafebabe", true},
		{"nightly", true},
		{"1.2", true},
		{"", false},
		{"unknown", false},
		{"1.2.3", false},
		{"v1.2.3", false},
		{"1.2.3-rc.1", false},
	} {
		if got := tc.writer.Development(); got != tc.want {
			t.Errorf("Writer(%q).Development() = %v, want %v", tc.writer, got, tc.want)
		}
	}
}

func TestWrittenByNewer(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		mine, theirs WrittenBy
		want         bool
	}{
		{"1.3.0", "1.2.9", true},
		{"1.2.3", "1.2.3", false},
		{"1.2.3", "1.3.0", false},
		{"1.2.3", "1.2.3-rc.1", true},
		{"1.2.3-rc.1", "1.2.3", false},
		{"dev+cafe", "1.2.3", false},
		{"1.2.3", "dev+cafe", false},
	} {
		if got := tc.mine.Newer(tc.theirs); got != tc.want {
			t.Errorf("Writer(%q).Newer(%q) = %v, want %v", tc.mine, tc.theirs, got, tc.want)
		}
	}
}
