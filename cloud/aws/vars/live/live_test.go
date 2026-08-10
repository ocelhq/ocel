package live

import (
	"strings"
	"testing"
)

func TestRender_NamesTheSameMissingComponentEveryTime(t *testing.T) {
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
}

func TestRender_AManifestNamingNoKeysIsNoFile(t *testing.T) {
	out, err := Render(Manifest{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out != nil {
		t.Errorf("Render = %q, want nothing packaged for an app that declares no live value", out)
	}
}

func TestRenderParse_RoundTripsTheAddressAndOnlyTheAddress(t *testing.T) {
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
}

func TestRenderParse_ProductionPinsNoEnvironment(t *testing.T) {
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
}

func TestRenderParse_APreviewCarriesTheEnvironmentItsOverridesAreAddressedBy(t *testing.T) {
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
}

func TestParse_RefusesWhatItCannotRead(t *testing.T) {
	if _, err := Parse([]byte("{not json")); err == nil {
		t.Fatal("Parse absorbed a manifest it could not decode")
	}
}
