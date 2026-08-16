package deploy

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/vars"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
	"google.golang.org/protobuf/types/known/structpb"
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
		if got.Record.Link.GetSource() != "sst" {
			t.Errorf("source = %q, want the publisher's own provenance rather than ocel's", got.Record.Link.GetSource())
		}
		if got.Record.Type() != linksv1.LinkType_LINK_TYPE_POSTGRES {
			t.Errorf("type = %s, want the postgres shape the publisher wrote", got.Record.Type())
		}
		if got.Record.Version != 4 {
			t.Errorf("version = %d, want the version the record was published at", got.Record.Version)
		}
		if _, read := consumed["bucket--uploads"]; read {
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

	t.Run("a record of another shape than the declaration is refused before it deploys", func(t *testing.T) {
		t.Parallel()
		cfg := consumingConfig("main")
		cfg.Links = &recordingPublisher{
			published: map[string][]string{varsClass: {"main"}},
			resolved: map[string]vars.PublishedRecord{"main": {
				Link:    &linksv1.Link{Source: "sst", Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: "main"}}},
				Version: 2,
			}},
		}

		_, err := consumeLinks(context.Background(), cfg, consumingManifest(), func(string) {})
		var refusal *LinkShapeError
		if !errors.As(err, &refusal) {
			t.Fatalf("consumeLinks err = %v, want a *LinkShapeError rather than a cold start that fails in production", err)
		}
		if refusal.Declared != linksv1.LinkType_LINK_TYPE_POSTGRES || refusal.Published != linksv1.LinkType_LINK_TYPE_BUCKET {
			t.Errorf("refusal = %+v, want it to name the declared and the published shapes", refusal)
		}
		for _, want := range []string{`"main"`, linksv1.LinkType_LINK_TYPE_POSTGRES.String(), linksv1.LinkType_LINK_TYPE_BUCKET.String()} {
			if !strings.Contains(refusal.Error(), want) {
				t.Errorf("refusal = %q, want it to carry %q", refusal.Error(), want)
			}
		}
	})

	t.Run("a custom record bound through `links` is refused before it deploys", func(t *testing.T) {
		t.Parallel()
		custom, err := structpb.NewStruct(map[string]any{"subnetIds": []any{"subnet-0a1"}})
		if err != nil {
			t.Fatalf("build the published struct: %v", err)
		}
		cfg := consumingConfig("main")
		cfg.Links = &recordingPublisher{
			published: map[string][]string{varsClass: {"main"}},
			resolved: map[string]vars.PublishedRecord{"main": {
				Link:    &linksv1.Link{Name: "main", Source: "sst", Properties: &linksv1.Link_Custom{Custom: custom}},
				Version: 2,
			}},
		}

		_, err = consumeLinks(context.Background(), cfg, consumingManifest(), func(string) {})
		var refusal *CustomLinkBoundError
		if !errors.As(err, &refusal) {
			t.Fatalf("consumeLinks err = %v, want a *CustomLinkBoundError", err)
		}
		if !strings.Contains(refusal.Error(), "read by transforms") || !strings.Contains(refusal.Error(), "never provisioned") {
			t.Errorf("refusal = %q, want it to say where a custom link is read instead", refusal.Error())
		}
	})

	t.Run("a bucket somebody else provisioned is refused", func(t *testing.T) {
		t.Parallel()
		manifest := linkedManifest()
		manifest.GetResources()[1].Linked = true
		cfg := consumingConfig("uploads")
		cfg.Links = &recordingPublisher{
			published: map[string][]string{varsClass: {"uploads"}},
			resolved: map[string]vars.PublishedRecord{"uploads": {
				Link:    &linksv1.Link{Source: "sst", Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: "sst-uploads"}}},
				Version: 2,
			}},
		}

		_, err := consumeLinks(context.Background(), cfg, manifest, func(string) {})
		var refusal *LinkSourceError
		if !errors.As(err, &refusal) {
			t.Fatalf("consumeLinks err = %v, want a *LinkSourceError: ocel's bucket client reads only what ocel provisioned", err)
		}
		if refusal.Source != "sst" || refusal.Link != "uploads" {
			t.Errorf("refusal = %+v, want it to name the link and who published it", refusal)
		}
		if !strings.Contains(refusal.Error(), "cannot serve a bucket it did not provision") {
			t.Errorf("refusal = %q, want it to say why the bucket is unusable", refusal.Error())
		}
	})

	t.Run("a bucket published without a source is consumed", func(t *testing.T) {
		t.Parallel()
		manifest := linkedManifest()
		manifest.GetResources()[1].Linked = true
		consumed := mustConsume(t, consumingConfig("uploads"), manifest)
		if consumed["bucket--uploads"].Record.Link.GetBucket().GetBucket() != "sst-uploads" {
			t.Errorf("consumed = %+v, want the bucket record read through", consumed)
		}
	})

	t.Run("a postgres published by another tool is consumed", func(t *testing.T) {
		t.Parallel()
		consumed := mustConsume(t, consumingConfig("main"), consumingManifest())
		if consumed["db--main"].Record.Link.GetSource() != "sst" {
			t.Errorf("consumed = %+v, want the foreign postgres read through", consumed)
		}
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
		{Name: "main", Key: "OCEL_RESOURCE_POSTGRES_main", Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Granted: 4},
		{Name: "bucket--uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: linksv1.LinkType_LINK_TYPE_BUCKET},
	}
	if !reflect.DeepEqual(links, want) {
		t.Errorf("appLinks = %+v, want %+v: a bound link is addressed at the key its app already reads, conformed against the shape its publisher wrote", links, want)
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

	policy, err := billedResourcePolicy(manifest.GetResources()[0], consumed, testSessions)
	if err != nil {
		t.Fatalf("billedResourcePolicy: %v", err)
	}
	if !strings.Contains(policy, "rds-db:connect") {
		t.Errorf("bill = %q, want a bound link billed at the grants it actually carries", policy)
	}
	if err := checkInlinePolicyBudget(manifest, consumed, testSessions); err != nil {
		t.Errorf("checkInlinePolicyBudget = %v, want two links well inside the ceiling", err)
	}
}

func TestAnUnscopedPublishedGrantIsRefusedBeforeAnyCloudCall(t *testing.T) {
	t.Parallel()
	manifest := consumingManifest()
	consumed := map[string]Consumed{"db--main": {
		Resource: "db--main",
		Record: vars.PublishedRecord{
			Link: &linksv1.Link{
				Name:       "main",
				Source:     "sst",
				Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{Host: "sst"}},
				Grants:     []*linksv1.Grant{{Label: "everything", Actions: []string{"s3:*"}, Resources: []string{"*"}}},
			},
			Version: 1,
		},
	}}

	var unscoped *UnscopedGrantError
	if err := checkInlinePolicyBudget(manifest, consumed, testSessions); !errors.As(err, &unscoped) {
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
		names = append(names, r.GetName())
	}
	if !slices.Equal(names, []string{"bucket--uploads"}) {
		t.Errorf("published = %v, want only what this deploy provisioned; the bound link belongs to whoever wrote it", names)
	}
	if publisher.owner != vars.OwnerOcel {
		t.Errorf("owner = %q, want ocel's publishes to prune only ocel's own inventory", publisher.owner)
	}
}
