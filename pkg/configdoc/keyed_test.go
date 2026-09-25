package configdoc

import (
	"encoding/json"
	"strings"
	"testing"
)

type lane struct {
	Width int `json:"width,omitempty" doc:"How wide the lane is."`
}

type road struct {
	Name string `json:"name" doc:"What the road is called."`
}

type route struct {
	Lane *lane `json:"lane,omitempty" doc:"Drive a lane."`
	Road *road `json:"road,omitempty" doc:"Drive a road."`
}

func (route) Shorthands() []string { return []string{"lane", "walk"} }

type trip struct {
	Route *route `json:"route,omitempty" doc:"How the trip goes."`
}

func TestAKeyedUnionIsAStringEnumOrOneObjectPerKey(t *testing.T) {
	generated, err := ProviderSchema[string, string]("acme", trip{}, nil, nil)
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
	if err := json.Unmarshal(fragment.Options.Properties["route"], &got); err != nil {
		t.Fatalf("unmarshal route: %v", err)
	}
	var want any
	if err := json.Unmarshal([]byte(`{
		"description": "How the trip goes.",
		"title": "route",
		"oneOf": [
			{"type": "string", "enum": ["lane", "walk"]},
			{"type": "object", "additionalProperties": false, "required": ["lane"], "properties": {"lane": {
				"description": "Drive a lane.", "title": "lane", "type": "object", "additionalProperties": false,
				"properties": {"width": {"description": "How wide the lane is.", "type": "integer"}}
			}}},
			{"type": "object", "additionalProperties": false, "required": ["road"], "properties": {"road": {
				"description": "Drive a road.", "title": "road", "type": "object", "additionalProperties": false,
				"required": ["name"],
				"properties": {"name": {"description": "What the road is called.", "type": "string"}}
			}}}
		]
	}`), &want); err != nil {
		t.Fatal(err)
	}
	if spelled, wanted := canonicalJSON(t, got), canonicalJSON(t, want); spelled != wanted {
		t.Errorf("route schema =\n%s\nwant\n%s", spelled, wanted)
	}
}

func canonicalJSON(t *testing.T, value any) string {
	t.Helper()
	written, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(written)
}

func TestAKeyedUnionTakesAShorthandOrOneKnownKey(t *testing.T) {
	for _, value := range []any{
		"lane",
		"walk",
		map[string]any{"lane": map[string]any{"width": 3.0}},
		map[string]any{"lane": map[string]any{}},
		map[string]any{"road": map[string]any{"name": "A1"}},
	} {
		if err := Check("trip", trip{}, map[string]any{"route": value}); err != nil {
			t.Errorf("Check(route: %v) = %v, want it taken", value, err)
		}
	}
}

func TestAKeyedUnionRefusesAnythingButOneKnownKeyOrAShorthand(t *testing.T) {
	cases := map[string]struct {
		value any
		want  []string
	}{
		"a shorthand it does not list": {"road", []string{`"trip.route"`, `"lane", "walk"`, "lane, road"}},
		"no key":                       {map[string]any{}, []string{`"trip.route"`, "exactly one", "lane, road"}},
		"two keys":                     {map[string]any{"lane": map[string]any{}, "road": map[string]any{"name": "A1"}}, []string{`"trip.route"`, "exactly one", "lane, road"}},
		"a key it does not know":       {map[string]any{"river": map[string]any{}}, []string{"trip.route.river", "lane, road"}},
		"a key whose value is wrong":   {map[string]any{"lane": map[string]any{"width": "wide"}}, []string{`"trip.route.lane.width"`, "a number"}},
		"neither text nor an object":   {7.0, []string{`"trip.route"`, `"lane", "walk"`, "lane, road"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := Check("trip", trip{}, map[string]any{"route": c.value})
			if err == nil {
				t.Fatalf("Check(route: %v) = nil, want a refusal", c.value)
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Check(route: %v) = %q, want it to name %s", c.value, err, want)
				}
			}
		})
	}
}

func TestAKeyedUnionNamesItsKeysInTheOrderItDeclaresThem(t *testing.T) {
	if got, want := strings.Join(KeysOf(route{}), " "), "lane road"; got != want {
		t.Errorf("KeysOf(route) = %q, want %q", got, want)
	}
}
