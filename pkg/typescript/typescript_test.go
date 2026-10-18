package typescript

import (
	"encoding/json"
	"reflect"
	"testing"
)

type imported struct{}

type Core struct {
	Name string `json:"name"`
}

type served struct {
	Core
	Count   int64             `json:"count,omitempty"`
	Labels  map[string]string `json:"labels"`
	Parts   []part            `json:"parts"`
	Raw     json.RawMessage   `json:"raw,omitzero"`
	Shared  *imported         `json:"shared,omitempty"`
	Hidden  string            `json:"-"`
	Skipped []byte            `json:"skipped" ts:"-"`
}

type part struct {
	On bool `json:"on"`
}

func TestDeclareWritesAnInterfacePerStructInTheOrderTheRootReachesThem(t *testing.T) {
	t.Parallel()

	got, err := Declare("// header", []Import{
		{Type: reflect.TypeFor[Core](), Name: "Core", From: "./Core.mjs"},
		{Type: reflect.TypeFor[imported](), Name: "Shared", From: "./shared.mjs"},
	}, reflect.TypeFor[served]())
	if err != nil {
		t.Fatal(err)
	}
	want := `// header

import type { Core } from "./Core.mjs";
import type { Shared } from "./shared.mjs";

export interface Served extends Core {
  count?: number;
  labels: Record<string, string>;
  parts: Part[];
  raw?: unknown;
  shared?: Shared;
}

export interface Part {
  on: boolean;
}
`
	if got != want {
		t.Errorf("Declare() =\n%s\nwant\n%s", got, want)
	}
}

func TestDeclareRefusesAMapNotKeyedByStrings(t *testing.T) {
	t.Parallel()

	type keyedByNumber struct {
		Counts map[int]string `json:"counts"`
	}
	if _, err := Declare("", nil, reflect.TypeFor[keyedByNumber]()); err == nil {
		t.Error("Declare() = nil error, want a map keyed by numbers refused: JSON objects are keyed by strings")
	}
}
