package edge

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNameUnderStem(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		stem, script string
		want         bool
	}{
		{"the stem itself", "ocel-shop-preview", "ocel-shop-preview", true},
		{"a name segmented below it", "ocel-shop-preview", "ocel-shop-preview-web", true},
		{"two segments below it", "ocel-shop-preview", "ocel-shop-preview-web-api", true},
		{"a sibling sharing only characters", "ocel-shop-preview", "ocel-shop-previewer", false},
		{"another project's stem", "ocel-shop-preview", "ocel-other-preview", false},
		{"a sibling project slugged past ours", "ocel-shop-preview", "ocel-shopfoo-preview", false},
		{"a production worker of the same project", "ocel-shop-preview", "ocel-shop-prod-web", false},
		{"an empty stem matches nothing", "", "ocel-shop-preview", false},
		{"an empty name is nothing's", "ocel-shop-preview", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := NameUnderStem(tc.stem, tc.script); got != tc.want {
				t.Errorf("NameUnderStem(%q, %q) = %v, want %v", tc.stem, tc.script, got, tc.want)
			}
		})
	}
}

func TestDeploymentRecord(t *testing.T) {
	t.Parallel()

	t.Run("audit fields are omitted when absent", func(t *testing.T) {
		t.Parallel()

		raw, err := json.Marshal(DeploymentRecord{App: "web", Identity: "b1"})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		for _, absent := range []string{"valueFingerprint", "variables"} {
			if strings.Contains(string(raw), absent) {
				t.Errorf("record = %s, want no %q for a Deployment that baked nothing", raw, absent)
			}
		}
	})

	t.Run("audit fields marshal under their wire names", func(t *testing.T) {
		t.Parallel()

		raw, err := json.Marshal(DeploymentRecord{
			App:              "web",
			Identity:         "b1~fp",
			ValueFingerprint: "fp",
			Variables: []VariableRecord{
				{Key: "PLAIN_KEY", Version: 2},
				{Key: "LIVE_KEY", Folder: "/api", Live: true},
			},
		})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}

		var got struct {
			ValueFingerprint string           `json:"valueFingerprint"`
			Variables        []VariableRecord `json:"variables"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if got.ValueFingerprint != "fp" {
			t.Errorf("valueFingerprint = %q, want %q", got.ValueFingerprint, "fp")
		}
		if len(got.Variables) != 2 {
			t.Fatalf("variables = %v, want both entries", got.Variables)
		}
		if got.Variables[0] != (VariableRecord{Key: "PLAIN_KEY", Version: 2}) {
			t.Errorf("variables[0] = %+v, want the version it shipped at", got.Variables[0])
		}
		if got.Variables[1] != (VariableRecord{Key: "LIVE_KEY", Folder: "/api", Live: true}) {
			t.Errorf("variables[1] = %+v, want a latest-at-runtime entry with no version", got.Variables[1])
		}
	})
}

type sampleAdapterState struct {
	Distribution string `json:"distribution,omitempty"`
	Region       string `json:"region,omitempty"`
}

func TestStackState(t *testing.T) {
	t.Parallel()

	settled := func() StackState {
		state := StackState{
			Slug:          "shop",
			Class:         ClassProduction,
			Endpoint:      "https://store.example",
			Secret:        "s3cr3t",
			OwnerToken:    "owner",
			Front:         "d123.cloudfront.net",
			GlobalPreview: "preview.acme.com",
			Adapter:       Own(sampleAdapterState{Distribution: "E123", Region: "eu-west-1"}),
		}
		state.Bind("shop.app.com")
		state.PublishFront("shop.app.com", "d-shop.example.net")
		state.RecordWrites([]Record{{Name: "shop.app.com", Type: RecordTypeCNAME, Value: "d123.cloudfront.net"}})
		return state
	}

	t.Run("a state nothing wrote to is empty and one anything wrote to is not", func(t *testing.T) {
		t.Parallel()

		if !(StackState{}).Empty() {
			t.Error("a zero state reports itself non-empty; the origin reads it as a project that has deployed")
		}
		for name, state := range map[string]StackState{
			"a slug":     {Slug: "shop"},
			"a front":    {Front: "d123.cloudfront.net"},
			"an adapter": {Adapter: Own(sampleAdapterState{Distribution: "E123"})},
		} {
			if state.Empty() {
				t.Errorf("a state carrying %s reports itself empty", name)
			}
		}
	})

	t.Run("everything a stack keeps survives the one encoding it is persisted through", func(t *testing.T) {
		t.Parallel()

		payload, err := json.Marshal(settled())
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var read StackState
		if err := json.Unmarshal(payload, &read); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if !read.Equal(settled()) {
			t.Errorf("state read back = %+v, want %+v", read, settled())
		}

		var own sampleAdapterState
		if err := read.Adapter.Into(&own); err != nil {
			t.Fatalf("Into: %v", err)
		}
		if own != (sampleAdapterState{Distribution: "E123", Region: "eu-west-1"}) {
			t.Errorf("adapter state = %+v, want the one the edge kept", own)
		}
	})

	t.Run("the contract never reads what the edge keeps to itself", func(t *testing.T) {
		t.Parallel()

		payload, err := json.Marshal(StackState{Slug: "shop", Adapter: Own(sampleAdapterState{Distribution: "E123"})})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if !strings.Contains(string(payload), `"adapter":{"distribution":"E123"}`) {
			t.Errorf("payload = %s, want the edge's own state under one slot of its own", payload)
		}
	})

	t.Run("a change anywhere is reported, and no change is not", func(t *testing.T) {
		t.Parallel()

		if !settled().Equal(settled()) {
			t.Error("two states built the same way are reported different; the origin would rewrite the store on every call")
		}
		for name, change := range map[string]func(*StackState){
			"a slug":           func(s *StackState) { s.Slug = "other" },
			"a class":          func(s *StackState) { s.Class = ClassPreview },
			"a secret":         func(s *StackState) { s.Secret = "rotated" },
			"a front":          func(s *StackState) { s.Front = "d456.cloudfront.net" },
			"a host front":     func(s *StackState) { s.PublishFront("shop.app.com", "moved.example.net") },
			"a bound domain":   func(s *StackState) { s.Bind("www.app.com") },
			"a written record": func(s *StackState) { s.RecordWrites(nil) },
			"a global preview": func(s *StackState) { s.GlobalPreview = "" },
			"the edge's own state": func(s *StackState) {
				s.Adapter = Own(sampleAdapterState{Distribution: "E456", Region: "eu-west-1"})
			},
		} {
			changed := settled()
			change(&changed)
			if changed.Equal(settled()) {
				t.Errorf("%s changed and the state reports itself unchanged; the origin persists only what a call reports", name)
			}
		}
	})

	t.Run("an unread state and one read back from nothing carry nothing", func(t *testing.T) {
		t.Parallel()

		var own sampleAdapterState
		if err := (StackState{}).Adapter.Into(&own); err != nil {
			t.Fatalf("Into on a state no edge wrote: %v", err)
		}
		if own != (sampleAdapterState{}) {
			t.Errorf("adapter state = %+v, want nothing", own)
		}
	})
}
