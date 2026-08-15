package deploy

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/vars"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
)

func consumingManifest() *deploymentsv1.Manifest {
	manifest := linkedManifest()
	manifest.GetResources()[0].Linked = true
	return manifest
}

func consumingConfig(published ...string) Config {
	cfg := liveConfig()
	cfg.VarsSiblingClasses = []string{"production", "preview"}
	cfg.Links = &recordingPublisher{published: map[string][]string{varsClass: published}}
	return cfg
}

func mustConsume(t *testing.T, cfg Config, manifest *deploymentsv1.Manifest) map[string]Consumed {
	t.Helper()
	consumed, err := consumeLinks(context.Background(), cfg, manifest, func(string) {})
	if err != nil {
		t.Fatalf("consumeLinks: %v", err)
	}
	return consumed
}

func TestConsumeLinks(t *testing.T) {
	t.Run("a bound resource carries the record the publisher wrote", func(t *testing.T) {
		t.Parallel()
		consumed := mustConsume(t, consumingConfig("main"), consumingManifest())

		got, bound := consumed["db--main"]
		if !bound {
			t.Fatalf("consumed = %+v, want the bound resource keyed by its logical name", consumed)
		}
		if got.Record.Type != "sst:aws.Postgres" {
			t.Errorf("type = %q, want the publisher's own token rather than the one ocel would have produced", got.Record.Type)
		}
		if got.Record.Version != 4 {
			t.Errorf("version = %d, want the version the record was published at", got.Record.Version)
		}
		if consumed["bucket--uploads"].Record.Type != "" {
			t.Errorf("consumed = %+v, want nothing read for the resource this deploy still provisions", consumed)
		}
	})

	t.Run("an unpublished name refuses the deploy and names its in-class siblings", func(t *testing.T) {
		t.Parallel()
		cfg := consumingConfig("orders", "sessions")
		_, err := consumeLinks(context.Background(), cfg, consumingManifest(), func(string) {})

		var refusal *UnpublishedLinkError
		if !errors.As(err, &refusal) {
			t.Fatalf("consumeLinks err = %v, want an *UnpublishedLinkError", err)
		}
		if !reflect.DeepEqual(refusal.Links, []string{"main"}) {
			t.Errorf("refused links = %v, want the one nothing published", refusal.Links)
		}
		message := refusal.Error()
		for _, want := range []string{`"main"`, "orders", "sessions", "production"} {
			if !strings.Contains(message, want) {
				t.Errorf("refusal = %q, want it to carry %q", message, want)
			}
		}
	})

	t.Run("a name published to another class is named as such", func(t *testing.T) {
		t.Parallel()
		cfg := liveConfig()
		cfg.VarsSiblingClasses = []string{"production", "preview"}
		cfg.Links = &recordingPublisher{published: map[string][]string{"preview": {"main"}}}

		_, err := consumeLinks(context.Background(), cfg, consumingManifest(), func(string) {})
		var refusal *UnpublishedLinkError
		if !errors.As(err, &refusal) {
			t.Fatalf("consumeLinks err = %v, want an *UnpublishedLinkError", err)
		}
		if !reflect.DeepEqual(refusal.FoundIn["main"], []string{"preview"}) {
			t.Errorf("cross-class probe = %v, want the class the record was published to instead", refusal.FoundIn)
		}
		if !strings.Contains(refusal.Error(), "published to preview instead") {
			t.Errorf("refusal = %q, want the probe's finding in the message", refusal.Error())
		}
	})

	t.Run("a provisioned name a publisher already claims is warned about", func(t *testing.T) {
		t.Parallel()
		var warnings []string
		cfg := consumingConfig("main", "uploads")
		if _, err := consumeLinks(context.Background(), cfg, consumingManifest(), func(w string) { warnings = append(warnings, w) }); err != nil {
			t.Fatalf("consumeLinks: %v", err)
		}
		if len(warnings) != 1 {
			t.Fatalf("warnings = %v, want exactly the provisioned name that collides", warnings)
		}
		if !strings.Contains(warnings[0], `"uploads"`) || !strings.Contains(warnings[0], "`links`") {
			t.Errorf("warning = %q, want it to name the collision and the fix", warnings[0])
		}
	})

	t.Run("a collision alone never fails the deploy", func(t *testing.T) {
		t.Parallel()
		manifest := linkedManifest()
		if _, err := consumeLinks(context.Background(), consumingConfig("main", "uploads"), manifest, func(string) {}); err != nil {
			t.Fatalf("consumeLinks err = %v, want a shadowed name to warn rather than refuse", err)
		}
	})

	t.Run("a record the app cannot be read through is refused before it deploys", func(t *testing.T) {
		t.Parallel()
		cfg := consumingConfig("main")
		cfg.Links = &recordingPublisher{
			published: map[string][]string{varsClass: {"main"}},
			resolved: map[string]vars.PublishedRecord{"main": {
				Record:  vars.Record{Type: "sst:aws.Postgres", Properties: map[string]string{"host": "db.example", "port": "5432"}},
				Version: 2,
			}},
		}

		_, err := consumeLinks(context.Background(), cfg, consumingManifest(), func(string) {})
		var refusal *LinkShapeError
		if !errors.As(err, &refusal) {
			t.Fatalf("consumeLinks err = %v, want a *LinkShapeError rather than a cold start that fails in production", err)
		}
		if !reflect.DeepEqual(refusal.Missing, []string{"connectionString"}) {
			t.Errorf("missing = %v, want the property the deployment reads and the record lacks", refusal.Missing)
		}
		for _, want := range []string{`"main"`, "connectionString", "host, port"} {
			if !strings.Contains(refusal.Error(), want) {
				t.Errorf("refusal = %q, want it to carry %q", refusal.Error(), want)
			}
		}
	})

	t.Run("a record carrying the shape the deployment reads is consumed", func(t *testing.T) {
		t.Parallel()
		mustConsume(t, consumingConfig("main"), consumingManifest())
	})

	t.Run("binding a link with no store to read it from is refused", func(t *testing.T) {
		t.Parallel()
		if _, err := consumeLinks(context.Background(), liveConfig(), consumingManifest(), func(string) {}); err == nil {
			t.Fatal("consumeLinks succeeded with no store; the app would cold-start against a record nobody checked")
		}
	})
}

func TestBindingAResourceOcelStillProvisionsIsRefused(t *testing.T) {
	t.Parallel()
	manifest := consumingManifest()

	err := handedOver(manifest, map[string]bool{"db--main": true, "bucket--uploads": true}, "shop-production")
	var refusal *HandoverError
	if !errors.As(err, &refusal) {
		t.Fatalf("handedOver = %v, want a *HandoverError; the next up would delete the database with no final snapshot", err)
	}
	if !reflect.DeepEqual(refusal.Links, []string{"main"}) {
		t.Errorf("refused = %v, want only the resource this deploy hands over", refusal.Links)
	}
	for _, want := range []string{`"main"`, "shop-production", "final snapshot", "`links`"} {
		if !strings.Contains(refusal.Error(), want) {
			t.Errorf("refusal = %q, want it to carry %q", refusal.Error(), want)
		}
	}

	if err := handedOver(manifest, map[string]bool{"bucket--uploads": true}, "shop-production"); err != nil {
		t.Errorf("handedOver = %v, want a link ocel never provisioned to bind freely", err)
	}
	if err := handedOver(linkedManifest(), map[string]bool{"db--main": true}, "shop-production"); err != nil {
		t.Errorf("handedOver = %v, want a deploy that binds nothing to pass untouched", err)
	}
}

func TestConsumedLinksReachAppsLikeProvisionedOnes(t *testing.T) {
	t.Parallel()
	manifest := consumingManifest()
	consumed := mustConsume(t, consumingConfig("main"), manifest)

	links := appLinks(manifest, "api", consumed)
	want := []live.Link{
		{Name: "main", Key: "OCEL_RESOURCE_POSTGRES_main", Type: "sst:aws.Postgres", Properties: []string{"connectionString"}, Granted: 4},
		{Name: "bucket--uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: naming.TokenBucket, Properties: []string{"bucket"}},
	}
	if !reflect.DeepEqual(links, want) {
		t.Errorf("appLinks = %+v, want %+v: a bound link is addressed at the key its app already reads, conformed against the token its publisher wrote", links, want)
	}

	if got := appLinks(manifest, "cron", consumed); len(got) != 0 {
		t.Errorf("appLinks(cron) = %+v, want a bound link scoped to the apps that use it, like any other resource", got)
	}

	policies, err := appLinkPolicies(manifest, "api", consumedLinks(consumed))
	if err != nil {
		t.Fatalf("appLinkPolicies: %v", err)
	}
	if len(policies) != 1 || policies[0].Link != "db--main" {
		t.Fatalf("policies = %+v, want one inline policy for the bound link", policies)
	}
	if !strings.Contains(policies[0].Policy, "rds-db:connect") {
		t.Errorf("policy = %q, want the grants the publisher wrote", policies[0].Policy)
	}

	if _, err := appLinkPolicies(manifest, "cron", consumedLinks(consumed)); err != nil {
		t.Fatalf("appLinkPolicies(cron): %v", err)
	}
}

func TestConsumedLinksAreBilledAgainstTheRoleCeiling(t *testing.T) {
	t.Parallel()
	manifest := consumingManifest()
	consumed := mustConsume(t, consumingConfig("main"), manifest)

	policy, err := billedResourcePolicy(manifest.GetResources()[0], consumed)
	if err != nil {
		t.Fatalf("billedResourcePolicy: %v", err)
	}
	if !strings.Contains(policy, "rds-db:connect") {
		t.Errorf("bill = %q, want a bound link billed at the grants it actually carries", policy)
	}
	if err := checkInlinePolicyBudget(manifest, consumed); err != nil {
		t.Errorf("checkInlinePolicyBudget = %v, want two links well inside the ceiling", err)
	}
}

func TestAnUnscopedPublishedGrantIsRefusedBeforeAnyCloudCall(t *testing.T) {
	t.Parallel()
	manifest := consumingManifest()
	consumed := map[string]Consumed{"db--main": {
		Resource: "db--main",
		Record: vars.PublishedRecord{
			Record:  vars.Record{Name: "main", Type: "sst:aws.Postgres", Grants: []vars.Grant{{Label: "everything", Actions: []string{"s3:*"}, Resources: []string{"*"}}}},
			Version: 1,
		},
	}}

	var unscoped *UnscopedGrantError
	if err := checkInlinePolicyBudget(manifest, consumed); !errors.As(err, &unscoped) {
		t.Fatalf("checkInlinePolicyBudget = %v, want an *UnscopedGrantError: a publisher may not hand an app blanket access", err)
	}
}

func TestOcelNeverRepublishesABoundLink(t *testing.T) {
	t.Parallel()
	publisher := &recordingPublisher{published: map[string][]string{varsClass: {"main"}}}
	cfg := liveConfig()
	cfg.Links = publisher

	if err := publishLinkRecords(context.Background(), cfg, consumingManifest(), provisionedLinks()); err != nil {
		t.Fatalf("publishLinkRecords: %v", err)
	}
	names := make([]string, 0, len(publisher.records))
	for _, r := range publisher.records {
		names = append(names, r.Name)
	}
	if !slices.Equal(names, []string{"bucket--uploads"}) {
		t.Errorf("published = %v, want only what this deploy provisioned; the bound link belongs to whoever wrote it", names)
	}
	if publisher.owner != vars.OwnerOcel {
		t.Errorf("owner = %q, want ocel's publishes to prune only ocel's own inventory", publisher.owner)
	}
}
