package live

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	t.Parallel()

	t.Run("names the same missing component every time", func(t *testing.T) {
		t.Parallel()

		m := Manifest{Keys: []Key{{Key: "DB_PASSWORD"}}}

		_, first := Render(m)
		if first == nil {
			t.Fatal("Render = nil, want a manifest naming keys it has no address for refused")
		}
		for range 50 {
			_, err := Render(m)
			if err == nil || err.Error() != first.Error() {
				t.Fatalf("Render reported %v, then %v, for the same manifest", first, err)
			}
		}
		if !strings.Contains(first.Error(), "project slug") {
			t.Errorf("error = %v, want the first component the manifest is missing", first)
		}
	})

	t.Run("a manifest naming no keys is no file", func(t *testing.T) {
		t.Parallel()

		out, err := Render(Manifest{})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if out != nil {
			t.Errorf("Render = %q, want nothing packaged for an app that declares no live value", out)
		}
	})
}

func TestRenderParse(t *testing.T) {
	t.Parallel()

	t.Run("round trips the address and only the address", func(t *testing.T) {
		t.Parallel()

		want := Manifest{
			Slug:   "shop",
			Table:  "ocel-vars",
			KeyARN: "arn:aws:kms:us-east-1:1234:key/abcd",
			Class:  "production",
			Keys:   []Key{{Key: "DB_PASSWORD"}, {Key: "SESSION_SECRET", Folder: "/web"}},
		}

		raw, err := Render(want)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if strings.Contains(string(raw), `"folder":""`) {
			t.Errorf("rendered %s, which writes the root folder out; empty is the absence of one", raw)
		}

		got, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if got.Slug != want.Slug || got.Table != want.Table || got.KeyARN != want.KeyARN || got.Class != want.Class {
			t.Errorf("Parse = %+v, want %+v", got, want)
		}
		if len(got.Keys) != 2 || got.Keys[0] != want.Keys[0] || got.Keys[1] != want.Keys[1] {
			t.Errorf("keys = %+v, want %+v in the order the deploy pinned them", got.Keys, want.Keys)
		}
	})

	t.Run("production pins no environment", func(t *testing.T) {
		t.Parallel()

		raw, err := Render(Manifest{
			Slug: "shop", Table: "ocel-vars", KeyARN: "arn:key", Class: "production",
			Keys: []Key{{Key: "DB_PASSWORD"}},
		})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if strings.Contains(string(raw), "environment") {
			t.Errorf("rendered %s, which names an environment for a class that has only one", raw)
		}

		got, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if got.Environment != "" {
			t.Errorf("environment = %q, want none", got.Environment)
		}
	})

	t.Run("a preview carries the environment its overrides are addressed by", func(t *testing.T) {
		t.Parallel()

		raw, err := Render(Manifest{
			Slug: "shop", Table: "ocel-vars", KeyARN: "arn:key", Class: "preview",
			Environment: "pr-42",
			Keys:        []Key{{Key: "DB_PASSWORD"}},
		})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		got, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if got.Environment != "pr-42" {
			t.Errorf("environment = %q, want pr-42 — without it the runtime reads class-wide and the override is dead", got.Environment)
		}
	})
}

func TestParse(t *testing.T) {
	t.Parallel()

	t.Run("refuses what it cannot read", func(t *testing.T) {
		t.Parallel()

		if _, err := Parse([]byte("{not json")); err == nil {
			t.Fatal("Parse absorbed a manifest it could not decode")
		}
	})
}
