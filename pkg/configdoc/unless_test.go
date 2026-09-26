package configdoc

import (
	"encoding/json"
	"strings"
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

type kilnFreedTwoWays struct {
	Preset string `json:"preset,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Heat   int    `json:"heat" unless:"preset"`
	Door   string `json:"door" unless:"mode"`
}

type kilnFreedByNothing struct {
	Heat int `json:"heat" unless:"presets"`
}

func TestAnUnlessTagTheSchemaCannotHonourFailsTheSchemaNamingIt(t *testing.T) {
	for name, tc := range map[string]struct {
		options any
		mention []string
	}{
		"two fields freed by different fields": {
			options: struct {
				Kiln kilnFreedTwoWays `json:"kiln"`
			}{},
			mention: []string{"kilnFreedTwoWays", `"heat"`, `"preset"`, `"door"`, `"mode"`},
		},
		"a field freed by no field": {
			options: struct {
				Kiln kilnFreedByNothing `json:"kiln"`
			}{},
			mention: []string{"kilnFreedByNothing", `"heat"`, `"presets"`},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ProviderSchema[string, string]("acme", tc.options, nil, nil)
			if err == nil {
				t.Fatalf("ProviderSchema(%T) = nil, want the unless tag refused", tc.options)
			}
			for _, mention := range tc.mention {
				if !strings.Contains(err.Error(), mention) {
					t.Errorf("ProviderSchema(%T) = %v, want it to mention %s", tc.options, err, mention)
				}
			}
		})
	}
}
