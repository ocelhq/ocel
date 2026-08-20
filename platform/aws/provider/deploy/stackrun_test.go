package deploy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/config"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestStackTags(t *testing.T) {
	t.Parallel()

	release := naming.NewRelease("B1", "abc123")

	t.Run("an app stack carries every fact constant across it", func(t *testing.T) {
		t.Parallel()

		cfg := Config{Slug: "shop", Tier: environmentv1.Tier_TIER_PRODUCTION}
		stack := naming.AppStack("prod", "web", release)

		tags := stackTags(cfg, stack, "p7", "d1", "B1")

		want := map[string]string{
			"ocel:managed-by": managedBy(),
			"ocel:project":    "shop",
			"ocel:env":        "prod",
			"ocel:env-class":  "production",
			"ocel:app":        "web",
			"ocel:release":    release.String(),
			"ocel:build":      "B1",
			"ocel:deployment": "d1",
			"ocel:promotion":  "p7",
			"ocel:stack":      stack.String(),
		}
		if !reflect.DeepEqual(tags, want) {
			t.Errorf("stackTags = %v, want %v", tags, want)
		}
	})

	t.Run("together with the resource's own tags the set is complete", func(t *testing.T) {
		t.Parallel()

		cfg := Config{Slug: "shop", Tier: environmentv1.Tier_TIER_PREVIEW, ExpiresAt: 1760000000}
		stack := naming.AppStack("pr-7", "web", release)

		keys := map[string]bool{}
		for key := range stackTags(cfg, stack, "p7", "d1", "B1") {
			keys[key] = true
		}
		for key := range resourceTags(naming.KindFunction, "/api/users", nil) {
			keys[key] = true
		}

		for _, key := range []string{
			"ocel:managed-by", "ocel:project", "ocel:env", "ocel:env-class", "ocel:app",
			"ocel:release", "ocel:build", "ocel:deployment", "ocel:promotion", "ocel:component", "ocel:route",
			"ocel:stack", "ocel:expires-at",
		} {
			if !keys[key] {
				t.Errorf("no resource carries %s", key)
			}
		}
	})

	t.Run("a preview is classed as such and stamped with its expiry", func(t *testing.T) {
		t.Parallel()

		cfg := Config{Slug: "shop", Tier: environmentv1.Tier_TIER_PREVIEW, ExpiresAt: 1760000000}

		tags := stackTags(cfg, naming.AppStack("pr-7", "web", release), "p7", "d1", "B1")

		if got, want := tags["ocel:env-class"], "preview"; got != want {
			t.Errorf("ocel:env-class = %q, want %q", got, want)
		}
		if got, want := tags["ocel:expires-at"], "1760000000"; got != want {
			t.Errorf("ocel:expires-at = %q, want %q", got, want)
		}
	})

	t.Run("the infra stack names itself and claims nothing that changes between deploys", func(t *testing.T) {
		t.Parallel()

		cfg := Config{Slug: "shop", Tier: environmentv1.Tier_TIER_PRODUCTION}

		tags := infraStackTags(cfg, naming.InfraStack("prod"))

		if got, want := tags["ocel:stack"], "prod--infra"; got != want {
			t.Errorf("ocel:stack = %q, want %q", got, want)
		}
		for _, key := range []string{"ocel:release", "ocel:build", "ocel:promotion", "ocel:route", "ocel:component"} {
			if _, ok := tags[key]; ok {
				t.Errorf("infra stack carries %s = %q, want it absent", key, tags[key])
			}
		}
	})

	t.Run("managed-by names the tool and a version AWS accepts in a tag", func(t *testing.T) {
		t.Parallel()

		got := managedBy()
		if !strings.HasPrefix(got, "ocel-cli/") {
			t.Fatalf("managedBy = %q, want an ocel-cli/<version>", got)
		}
		for _, r := range got {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			case strings.ContainsRune("+-=._:/@ ", r):
			default:
				t.Errorf("managedBy = %q, has %q which AWS rejects in a tag value", got, r)
			}
		}
	})
}

func TestDefaultTagsReachTheWholeProgram(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(map[string]map[string]string{"tags": {
		"ocel:project":    "shop",
		"ocel:expires-at": "1760000000",
	}})
	if err != nil {
		t.Fatal(err)
	}

	settings := &workspace.ProjectStack{Config: config.Map{
		config.MustMakeKey("aws", "defaultTags"): config.NewObjectValue(string(encoded)),
	}}
	path := filepath.Join(t.TempDir(), "Pulumi.prod--web--r3f8a1c9.yaml")
	if err := settings.Save(path); err != nil {
		t.Fatalf("save stack settings: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"aws:defaultTags", "ocel:project: shop", `ocel:expires-at: "1760000000"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("stack settings\n%s\nmissing %q", raw, want)
		}
	}
}

func TestEmitEngineTraceAttachesResourceIdentityOnlyToStandouts(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	parent := NewRootStage("Provisioning").ID
	start := time.Unix(6000, 0)
	trace := EngineTrace{
		ResourceCount: 2,
		Start:         start,
		End:           start.Add(5 * time.Second),
		Failed:        true,
		Standouts: []ResourceStandout{
			{
				Op:     apitype.OpCreate,
				Type:   "aws:s3/bucket:Bucket",
				Name:   "my-bucket",
				Start:  start,
				End:    start.Add(5 * time.Second),
				Failed: true,
			},
		},
	}

	emitEngineTrace(ft, parent, trace, nil)

	if len(ft.spans) != 2 {
		t.Fatalf("got %d spans, want 2 (batch + standout)", len(ft.spans))
	}

	batch := ft.spans[0]
	if batch.name != engineBatchSpanName {
		t.Fatalf("spans[0].name = %q, want the batch span name", batch.name)
	}
	for _, a := range batch.attrs {
		if a.Key == progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE || a.Key == progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME {
			t.Errorf("batch span carries resource identity attr %+v; it covers many resources", a)
		}
	}

	standout := ft.spans[1]
	var sawType, sawName bool
	for _, a := range standout.attrs {
		switch a.Key {
		case progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE:
			sawType = true
			if a.Value != "aws:s3/bucket:Bucket" {
				t.Errorf("RESOURCE_TYPE = %q, want the type token", a.Value)
			}
		case progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME:
			sawName = true
			if a.Value != "my-bucket" {
				t.Errorf("RESOURCE_NAME = %q, want the logical name", a.Value)
			}
			if strings.Contains(a.Value, "urn:pulumi") {
				t.Fatal("RESOURCE_NAME carried the raw URN")
			}
		}
	}
	if !sawType {
		t.Error("standout span is missing ATTRIBUTE_KEY_RESOURCE_TYPE")
	}
	if !sawName {
		t.Error("standout span is missing ATTRIBUTE_KEY_RESOURCE_NAME")
	}
}

func TestEmitEngineTraceOmitsResourceIdentityWhenTheURNDidNotParse(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	parent := NewRootStage("Provisioning").ID
	start := time.Unix(7000, 0)
	trace := EngineTrace{
		ResourceCount: 1,
		Start:         start,
		End:           start.Add(time.Second),
		Failed:        true,
		Standouts: []ResourceStandout{
			{Op: apitype.OpCreate, Type: "", Name: "", Start: start, End: start.Add(time.Second), Failed: true},
		},
	}

	emitEngineTrace(ft, parent, trace, nil)

	if len(ft.spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(ft.spans))
	}
	for _, a := range ft.spans[1].attrs {
		if a.Key == progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE || a.Key == progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME {
			t.Errorf("standout span carries resource identity attr %+v despite an unparseable URN", a)
		}
	}
}

func TestEmitEngineTraceStillEmitsOnAnUpErrorWithNoResourceOperations(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	parent := NewRootStage("Provisioning").ID
	start := time.Unix(8000, 0)
	trace := EngineTrace{Start: start, End: start.Add(time.Second)}

	emitEngineTrace(ft, parent, trace, errors.New("plugin failed to start"))

	if len(ft.spans) != 1 {
		t.Fatalf("got %d spans, want 1: a Pulumi run that failed before touching a resource must still leave a span", len(ft.spans))
	}
	if ft.spans[0].err == nil {
		t.Error("batch span not recorded as failed")
	}
}

func TestEmitEngineTraceStaysSilentOnAQuietSuccessfulRun(t *testing.T) {
	t.Parallel()

	ft := &fakeTracer{}
	parent := NewRootStage("Provisioning").ID
	trace := EngineTrace{}

	emitEngineTrace(ft, parent, trace, nil)

	if len(ft.spans) != 0 {
		t.Fatalf("got %d spans, want 0: nothing happened and nothing failed", len(ft.spans))
	}
}

func TestAwaitEngineTraceReturnsWithinGraceOnAChannelThatIsNeverSentTo(t *testing.T) {
	t.Parallel()

	result := make(chan EngineTrace)
	done := make(chan EngineTrace, 1)
	go func() { done <- awaitEngineTrace(result, 20*time.Millisecond) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("awaitEngineTrace did not return within its grace period")
	}
}

func TestStartEngineTraceDrainDoesNotBlockWhenEngineEventsIsNeverClosed(t *testing.T) {
	t.Parallel()

	engineEvents := make(chan events.EngineEvent, 4)
	engineEvents <- events.EngineEvent{}

	result := startEngineTraceDrain(engineEvents, 0)

	trace := awaitEngineTrace(result, 50*time.Millisecond)
	if !reflect.DeepEqual(trace, EngineTrace{}) {
		t.Errorf("got %+v, want a zero-value trace: the channel is unclosed so the builder goroutine never sent a result", trace)
	}
}
