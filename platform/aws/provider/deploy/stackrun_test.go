package deploy

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
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

func TestTheDefaultTagsTheProgramAsksForCarryEveryStackFact(t *testing.T) {
	t.Parallel()

	cfg := Config{Slug: "shop", Tier: environmentv1.Tier_TIER_PREVIEW, ExpiresAt: 1760000000}
	stack := naming.AppStack("pr-7", "web", naming.NewRelease("B1", "abc123"))
	tags := stackTags(cfg, stack, "p7", "d1", "B1")

	configured, err := NewReleaser(cfg, &Realized{}).Configure(context.Background(), providerkit.StackPlan{
		Ref:  refFor(cfg, stack),
		Tags: tags,
	})
	if err != nil {
		t.Fatalf("Configure() = %v", err)
	}

	var carried map[string]map[string]string
	if err := json.Unmarshal([]byte(configured["aws:defaultTags"].Value), &carried); err != nil {
		t.Fatalf("read the default tags the program asked for: %v", err)
	}
	if !reflect.DeepEqual(carried["tags"], tags) {
		t.Errorf("default tags = %v, want every fact the stack carries: %v", carried["tags"], tags)
	}
}

func TestAStackWithNoTagsAsksForNoConfigAtAll(t *testing.T) {
	t.Parallel()

	cfg := Config{Slug: "shop"}
	configured, err := NewReleaser(cfg, &Realized{}).Configure(context.Background(), providerkit.StackPlan{
		Ref: refFor(cfg, naming.InfraStack("prod")),
	})
	if err != nil {
		t.Fatalf("Configure() = %v", err)
	}
	if len(configured) != 0 {
		t.Errorf("Configure() = %v, want nothing for a stack that claims no tags", configured)
	}
}
