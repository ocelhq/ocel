package names_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func TestAnAppBucketIsNamedUnderTheAppFenceWithItsRandomTagLast(t *testing.T) {
	bucket, err := names.New("ocel", "acme-prod").AppBucket("shop", "prod", "uploads", "k3x9q2")
	if err != nil {
		t.Fatal(err)
	}
	if bucket != "ocel-app-shop-prod-uploads-k3x9q2" {
		t.Errorf("AppBucket() = %q, want ocel-app-shop-prod-uploads-k3x9q2", bucket)
	}
}

func TestALongNamespaceNamesItsAppBucketsUnderItsCompactedField(t *testing.T) {
	bucket, err := names.New("northwind-traders", "acme-prod").AppBucket("shop", "prod", "uploads", "k3x9q2")
	if err != nil {
		t.Fatal(err)
	}
	if bucket != "north-fe0905-app-shop-prod-uploads-k3x9q2" {
		t.Errorf("AppBucket() = %q, want the namespace compacted to twelve characters ahead of the fence", bucket)
	}
}

func TestAnAppBucketTooLongForCloudStorageCutsTheEnvironmentFirst(t *testing.T) {
	bucket, err := names.New("ocel", "acme-prod").AppBucket("storefront", "feature-checkout-redesign-with-payments", "uploads", "k3x9q2")
	if err != nil {
		t.Fatal(err)
	}
	if len(bucket) > 63 || !strings.HasPrefix(bucket, "ocel-app-storefront-") || !strings.HasSuffix(bucket, "-uploads-k3x9q2") {
		t.Errorf("AppBucket() = %q (%d characters), want at most 63 with the project, the bucket and the tag kept whole", bucket, len(bucket))
	}
}

func TestAnAppBucketThatCannotFitCloudStorageIsRefused(t *testing.T) {
	if bucket, err := names.New("ocel", "acme-prod").AppBucket("shop", "prod", strings.Repeat("uploads", 8), "k3x9q2"); err == nil {
		t.Errorf("AppBucket() = %q, want a refusal: a bucket name is at most 63 characters and the declared name is never cut", bucket)
	}
}

func TestAnUntaggedAppBucketIsTheBucketWithoutItsTag(t *testing.T) {
	named := names.New("ocel", "acme-prod")
	project, env, declared := "storefront", "feature-checkout-redesign-with-payments", "uploads"
	bucket, err := named.AppBucket(project, env, declared, "k3x9q2")
	if err != nil {
		t.Fatal(err)
	}
	untagged, err := named.UntaggedAppBucket(project, env, declared)
	if err != nil {
		t.Fatal(err)
	}
	if untagged+"-k3x9q2" != bucket {
		t.Errorf("UntaggedAppBucket() = %q, want %q without its tag", untagged, bucket)
	}
}

func TestAPostgresInstanceIsNamedUnderTheAppFenceWithItsRandomTagLast(t *testing.T) {
	instance, err := names.New("ocel", "acme-prod").PostgresInstance("shop", "prod", "orders", "k3x9q2")
	if err != nil {
		t.Fatal(err)
	}
	if instance != "ocel-app-shop-prod-orders-k3x9q2" {
		t.Errorf("PostgresInstance() = %q, want ocel-app-shop-prod-orders-k3x9q2", instance)
	}
}

func TestAnAppsServiceIsNamedUnderTheAppFenceWithItsHashTagLast(t *testing.T) {
	named := names.New("ocel", "acme-prod")
	for function, want := range map[string]string{
		"web":               "ocel-app-shop-prod-web-f94747",
		"fn--web--checkout": "ocel-app-shop-pro-web-checkout-281fb1",
	} {
		service, err := named.Service("shop", "prod", "web", function)
		if err != nil {
			t.Fatal(err)
		}
		if service != want {
			t.Errorf("Service(%s) = %q, want %q", function, service, want)
		}
	}
}

func TestAServiceTooLongForARevisionTagCutsTheEnvironmentFirst(t *testing.T) {
	named := names.New("ocel", "acme-prod")
	service, err := named.Service("storefront", "feature-checkout", "web", "web")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(service, "ocel-app-storefront-featur-web-") || len(service) > 37 {
		t.Errorf("Service() = %q (%d characters), want the environment cut before the project to fit 37", service, len(service))
	}
	service, err = named.Service("storefront-emea", "feature-checkout", "web", "web")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(service, "ocel-app-storefront-em-fea-web-") || len(service) > 37 {
		t.Errorf("Service() = %q (%d characters), want the environment cut to three characters, then the project, to fit 37", service, len(service))
	}
}

func TestAServiceThatCannotFitARevisionTagIsRefused(t *testing.T) {
	if service, err := names.New("ocel", "acme-prod").Service("shop", "prod", "web", "fn--web--account-settings-billing"); err == nil {
		t.Errorf("Service() = %q, want a refusal: a route is never cut, and Cloud Run builds a tagged revision's url from 37 characters of the service", service)
	}
}

func TestAWorkerServiceIsNamedForItsAppAndWorkerUnderTheAppFence(t *testing.T) {
	service, err := names.New("ocel", "acme-prod").WorkerService("shop", "prod", "web", "media")
	if err != nil {
		t.Fatal(err)
	}
	if service != "ocel-app-shop-prod-web-media-3684dc" {
		t.Errorf("WorkerService() = %q, want ocel-app-shop-prod-web-media-3684dc", service)
	}
}

func TestARealtimeGatewayIsNamedForItsEnvironmentUnderTheAppFence(t *testing.T) {
	if gateway, err := names.New("ocel", "acme-prod").RealtimeGateway("shop", "prod"); err != nil || gateway != "ocel-app-shop-prod-realtime-660706" {
		t.Errorf("RealtimeGateway() = %q, %v, want ocel-app-shop-prod-realtime-660706", gateway, err)
	}
	if gateway, err := names.New("northwind-traders", "acme-prod").RealtimeGateway("feature-checkout-redesign", "storefront-emea-west"); err != nil || len(gateway) > 49 {
		t.Errorf("RealtimeGateway() = %q (%d characters), %v, want it cut to the 49 Cloud Run takes", gateway, len(gateway), err)
	}
}

func TestAKVStoreIsNamedForItsEnvironmentAndStoreUnderTheAppFence(t *testing.T) {
	instance, err := names.New("ocel", "acme-prod").KVInstance("shop", "prod", "cache")
	if err != nil {
		t.Fatal(err)
	}
	if instance != "ocel-app-shop-prod-cache-8bf31e" {
		t.Errorf("KVInstance() = %q, want ocel-app-shop-prod-cache-8bf31e", instance)
	}
}

func TestAnAppsServiceAccountIsItsNamespaceFenceAndAHashThatFitsGoogle(t *testing.T) {
	for _, namespace := range []provider.Namespace{"ocel", "northwind-traders-europe-west-production-blue"} {
		account := names.New(namespace, "acme-prod").AppAccount("feature-checkout-redesign", "storefront", "web")
		if !strings.HasPrefix(account, naming.AppFence(string(namespace))) || len(account) > names.MaxAccountID {
			t.Errorf("AppAccount() in namespace %s = %q (%d characters), want it under the app fence and at most %d", namespace, account, len(account), names.MaxAccountID)
		}
	}
}

func TestAnOcelOwnedNameIsTheNamespaceTheTierWordAndOcelsOwnWords(t *testing.T) {
	named := names.New("ocel", "acme-prod")
	prod, preview := environment.TierProduction, environment.TierPreview
	for got, want := range map[string]string{
		named.Repository(prod):                      "ocel-prod-containers",
		named.Network(preview):                      "ocel-preview-network",
		named.EnvSourceSync(prod):                   "ocel-prod-env-sync",
		named.Bastion(preview):                      "ocel-preview-bastion",
		named.DelayQueue(prod):                      "ocel-prod-delays",
		named.PassphraseSecret(prod):                "ocel-prod-pulumi-passphrase",
		named.EdgeCredentialsSecret(preview, "alb"): "ocel-preview-alb-edge-credentials",
		named.PurgeSeedSecret(prod, "alb"):          "ocel-prod-alb-purge-seed",
		named.OriginSecret(prod, "cloudflare"):      "ocel-prod-cloudflare-origin-secret",
		named.Connector():                           "ocel-connector",
		named.RealtimeAccount(prod):                 "ocel-prod-realtime",
		named.PushAccount(preview):                  "ocel-preview-push",
		named.EnvSourceSyncAccount(prod):            "ocel-prod-env-sync",
		named.BastionAccount(prod):                  "ocel-prod-bastion",
		named.AssetReaderAccount(preview):           "ocel-preview-assets",
		named.GateTierSecretPrefix(preview):         "ocel_gate-preview-",
		named.RealtimeKeysSecretPrefix(prod):        "ocel_realtime-keys-prod-",
		named.SharedDatabase(prod):                  "ocel-app-prod-shared",
		named.OwnDatabasePrefix(preview):            "ocel-app-preview-p-",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestEveryServiceAccountOfALongNamespaceFitsGoogle(t *testing.T) {
	named := names.New("northwind-traders-europe-west-production-blue", "acme-prod")
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		for _, account := range []string{named.RealtimeAccount(tier), named.PushAccount(tier), named.EnvSourceSyncAccount(tier), named.BastionAccount(tier), named.AssetReaderAccount(tier), named.Connector()} {
			if !strings.HasPrefix(account, "north-") || len(account) > names.MaxAccountID {
				t.Errorf("account %q is %d characters, want it under the namespace's field and at most %d", account, len(account), names.MaxAccountID)
			}
		}
	}
}

func TestNoAppDatabaseOfAnotherNamespaceIsNamedByThisNamespacesAppDatabaseNames(t *testing.T) {
	ours := names.New("ocel", "acme-prod")
	for _, namespace := range []provider.Namespace{"ocel-prod-p", "ocel-preview-p", "ocel-prod-shared", "ocel-app-prod"} {
		theirs := names.New(namespace, "acme-prod")
		for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
			for _, database := range []string{theirs.OwnDatabase(tier, "shop"), theirs.SharedDatabase(tier)} {
				if strings.HasPrefix(database, ours.OwnDatabasePrefix(tier)) || database == ours.SharedDatabase(tier) {
					t.Errorf("namespace %s names its database %q as namespace ocel names its own, so ocel's apps read and write its records", namespace, database)
				}
			}
		}
	}
}

func TestNoAppResourceOfAnotherNamespaceStartsWithThisNamespacesAppFence(t *testing.T) {
	fence := names.New("ocel", "acme-prod").AppFence()
	for _, namespace := range []provider.Namespace{"ocel-dev", "ocel-shop", "ocelot", "ocel-production-eu"} {
		other := names.New(namespace, "acme-prod")
		bucket, err := other.AppBucket("shop", "prod", "uploads", "k3x9q2")
		if err != nil {
			t.Fatal(err)
		}
		instance, err := other.PostgresInstance("shop", "prod", "orders", "k3x9q2")
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{bucket, instance} {
			if strings.HasPrefix(name, fence) {
				t.Errorf("namespace %s names %q under namespace ocel's fence %q, so ocel's deploy credential administers it", namespace, name, fence)
			}
		}
	}
}

func TestNoTierBucketFallsUnderTheAppFenceWhateverTheGoogleCloudProjectID(t *testing.T) {
	for _, project := range []string{"acme-prod", "app-shop", "app"} {
		named := names.New("ocel", project)
		for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
			for _, class := range names.TierStorageClasses {
				bucket := named.TierBucket(tier, class, "k3x9q2")
				if strings.HasPrefix(bucket, named.AppFence()) {
					t.Errorf("in Google Cloud project %s the %s %s bucket is %q, under the app fence %q the deploy credential administers buckets by", project, tier, class, bucket, named.AppFence())
				}
			}
		}
	}
}

func TestATierBucketIsNamedForItsTierWordAndClassWithItsRandomTagLast(t *testing.T) {
	named := names.New("ocel", "acme-prod")
	for tier, want := range map[environment.Tier]string{
		environment.TierProduction: "ocel-prod-edge-k3x9q2",
		environment.TierPreview:    "ocel-preview-edge-k3x9q2",
	} {
		if got := named.TierBucket(tier, naming.StorageEdge, "k3x9q2"); got != want {
			t.Errorf("TierBucket(%s, edge) = %q, want %q", tier, got, want)
		}
	}
	long := names.New("northwind-traders", "acme-prod")
	if got := long.TierBucket(environment.TierPreview, naming.StorageCache, "k3x9q2"); got != naming.NamespaceField("northwind-traders")+"-preview-cache-k3x9q2" {
		t.Errorf("TierBucket() of a long namespace = %q, want it under the namespace's compacted field", got)
	}
}
