package configdoc

import (
	"encoding/json"
	"testing"
)

type kiln struct {
	Name   string `json:"name" doc:"What the kiln is called."`
	Preset string `json:"preset,omitempty" enum:"pottery,glass" doc:"Fills every field the kiln needs."`
	Heat   int    `json:"heat" unless:"preset" doc:"How hot it fires."`
	Door   string `json:"door,omitempty" doc:"Which way the door opens."`
}

type workshop struct {
	Kiln *kiln `json:"kiln,omitempty" doc:"The kiln the workshop fires."`
}

func TestAFieldRequiredUnlessAPresetMakesTheObjectEitherThePresetOrTheFieldSpelledOut(t *testing.T) {
	generated, err := ProviderSchema[string, string]("acme", workshop{}, nil, nil)
	if err != nil {
		t.Fatalf("provider schema: %v", err)
	}
	var fragment struct {
		Options struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"options"`
	}
	if err := json.Unmarshal(generated, &fragment); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var got any
	if err := json.Unmarshal(fragment.Options.Properties["kiln"], &got); err != nil {
		t.Fatalf("unmarshal kiln: %v", err)
	}
	var want any
	if err := json.Unmarshal([]byte(`{
		"description": "The kiln the workshop fires.",
		"title": "kiln",
		"oneOf": [
			{"type": "object", "additionalProperties": false, "required": ["name", "preset"], "properties": {
				"name": {"description": "What the kiln is called.", "type": "string"},
				"preset": {"description": "Fills every field the kiln needs.", "type": "string", "enum": ["pottery", "glass"]},
				"heat": {"description": "How hot it fires.", "type": "integer"},
				"door": {"description": "Which way the door opens.", "type": "string"}
			}},
			{"type": "object", "additionalProperties": false, "required": ["name", "heat"], "properties": {
				"name": {"description": "What the kiln is called.", "type": "string"},
				"preset": false,
				"heat": {"description": "How hot it fires.", "type": "integer"},
				"door": {"description": "Which way the door opens.", "type": "string"}
			}}
		]
	}`), &want); err != nil {
		t.Fatal(err)
	}
	if spelled, wanted := canonicalJSON(t, got), canonicalJSON(t, want); spelled != wanted {
		t.Errorf("kiln schema =\n%s\nwant\n%s", spelled, wanted)
	}
}
