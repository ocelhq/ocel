package live

import (
	"strings"
	"testing"
)

// TestRender_NamesTheSameMissingComponentEveryTime is why the check is over an
// ordered list. A deploy against a half-bootstrapped account is missing several
// components at once, and an error that named a different one on each attempt
// would send the operator after a different fix each time.
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

// TestRender_AManifestNamingNoKeysIsNoFile proves the absence of live values is
// spelled as nothing at all, not as an empty manifest: the file's presence is
// what puts a store dependency on a function's cold path.
func TestRender_AManifestNamingNoKeysIsNoFile(t *testing.T) {
	out, err := Render(Manifest{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out != nil {
		t.Errorf("Render = %q, want nothing packaged for an app that declares no live value", out)
	}
}

// TestRenderParse_RoundTripsTheAddressAndOnlyTheAddress pins what crosses from
// the deploy to the sandbox. The root folder's single spelling is the trap: it
// is empty here and stays empty, because the store owns the sentinel it renders
// that as and a manifest that spelled one would be refused at every read.
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

// TestRenderParse_ProductionPinsNoEnvironment is the other half of the address:
// production has one environment, so the manifest names none and the runtime
// has no override to look for. The field must be absent rather than empty, for
// the reason the root folder is: a component the store spells itself is not one
// a package may carry a second spelling of.
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

// TestRenderParse_APreviewCarriesTheEnvironmentItsOverridesAreAddressedBy pins
// the one component that decides whether a runtime read looks for an override
// at all. It rides the manifest once rather than per key: every key in a
// package resolves for the same environment, because it is the deployment's
// identity and not the key's.
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

// TestParse_RefusesWhatItCannotRead proves a manifest that will not decode is
// an error rather than an empty one. Absorbing it would hand the sandbox a
// function with no addresses and no complaint, and the values would be read at
// the point of use as ones that were never required.
func TestParse_RefusesWhatItCannotRead(t *testing.T) {
	if _, err := Parse([]byte("{not json")); err == nil {
		t.Fatal("Parse absorbed a manifest it could not decode")
	}
}
