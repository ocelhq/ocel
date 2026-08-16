package live

import (
	"errors"
	"strings"
	"testing"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
)

func postgresLink() Link {
	return Link{
		Name: "db--main",
		Key:  "OCEL_RESOURCE_POSTGRES_main",
		Type: linksv1.LinkType_LINK_TYPE_POSTGRES,
	}
}

const postgresRecord = `{"name":"db--main","postgres":{"host":"h","port":5432,"database":"d","username":"u","password":"p"}}`

func TestConform(t *testing.T) {
	t.Parallel()

	t.Run("names a value that is not a record without repeating a byte of it", func(t *testing.T) {
		t.Parallel()

		const secret = "xq7#!zv%"
		link := postgresLink()
		err := Conform([]Link{link}, map[string]string{link.Key: secret})
		if err == nil {
			t.Fatal("Conform = nil, want a value that is not a record refused")
		}
		if !errors.Is(err, ErrDrift) {
			t.Errorf("error = %v, want it named as drift", err)
		}
		for _, b := range []byte(secret) {
			if strings.ContainsRune(err.Error(), rune(b)) {
				t.Errorf("error = %v, which carries %q from a value this deployment must treat as a credential", err, string(b))
			}
		}
		for _, want := range []string{link.Name, link.Key, link.Type.String()} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("names a record that carries no properties", func(t *testing.T) {
		t.Parallel()

		link := postgresLink()
		err := Conform([]Link{link}, map[string]string{link.Key: `{"name":"db--main"}`})
		if err == nil {
			t.Fatal("Conform = nil, want a record carrying no properties refused")
		}
		if !errors.Is(err, ErrDrift) {
			t.Errorf("error = %v, want it named as drift", err)
		}
		if strings.Contains(err.Error(), "  ") {
			t.Errorf("error = %v, which renders the missing token as a hole rather than naming it", err)
		}
		for _, want := range []string{link.Name, link.Key, link.Type.String()} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("names a record of another type", func(t *testing.T) {
		t.Parallel()

		link := postgresLink()
		err := Conform([]Link{link}, map[string]string{link.Key: `{"name":"db--main","bucket":{"bucket":"b"}}`})
		if err == nil {
			t.Fatal("Conform = nil, want a bucket record under a postgres link refused")
		}
		if !errors.Is(err, ErrDrift) {
			t.Errorf("error = %v, want it named as drift", err)
		}
		for _, want := range []string{link.Name, link.Key, link.Type.String(), linksv1.LinkType_LINK_TYPE_BUCKET.String()} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("names a link that published nothing", func(t *testing.T) {
		t.Parallel()

		link := postgresLink()
		err := Conform([]Link{link}, map[string]string{"DB_PASSWORD": "hunter2"})
		if err == nil {
			t.Fatal("Conform = nil, want a link with no record refused")
		}
		if !errors.Is(err, ErrDrift) {
			t.Errorf("error = %v, want it named as drift", err)
		}
	})

	t.Run("accepts a record of the declared type", func(t *testing.T) {
		t.Parallel()

		link := postgresLink()
		values := map[string]string{link.Key: postgresRecord, "DB_PASSWORD": "hunter2"}
		if err := Conform([]Link{link}, values); err != nil {
			t.Fatalf("Conform: %v", err)
		}
		if values[link.Key] != postgresRecord || values["DB_PASSWORD"] != "hunter2" {
			t.Errorf("values = %v, want everything left as published", values)
		}
	})
}

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

	t.Run("links carry their type by name", func(t *testing.T) {
		t.Parallel()

		want := postgresLink()
		want.Granted = 3
		raw, err := Render(Manifest{
			Slug: "shop", Table: "ocel-vars", KeyARN: "arn:key", Class: "production",
			Links: []Link{want},
		})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if !strings.Contains(string(raw), `"type":"LINK_TYPE_POSTGRES"`) {
			t.Errorf("rendered %s, want the link type written by its name", raw)
		}
		got, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if len(got.Links) != 1 || got.Links[0] != want {
			t.Errorf("links = %+v, want %+v", got.Links, []Link{want})
		}
	})

	t.Run("refuses a link type no link type is called", func(t *testing.T) {
		t.Parallel()

		if _, err := Parse([]byte(`{"slug":"shop","table":"t","keyArn":"k","class":"production","links":[{"name":"x","key":"K","type":"ocel:postgres"}]}`)); err == nil {
			t.Fatal("Parse absorbed a link type it does not know")
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
