package providerkit_test

import (
	"context"
	"strings"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

func declaring(req *contractv1.DeployRequest, class resourcesv1.VariableClass, key, value string) *contractv1.DeployRequest {
	req.Manifest.Apps[0].Variables = append(req.Manifest.Apps[0].Variables, &contractv1.ManifestVariable{
		Key: key, Value: value, Class: class,
	})
	return req
}

func sealValue(t *testing.T, p *fake.Provider, key, plaintext string) {
	t.Helper()
	store := values.Store{Records: p.Records(), Sealer: p.Sealer()}
	scope := values.Scope{Project: "shop", Class: providerkit.ClassProduction}
	if _, err := store.Set(context.Background(), scope, values.Coordinate{Cell: values.Cell{Key: key}}, plaintext, nil); err != nil {
		t.Fatalf("Set(%s): %v", key, err)
	}
}

func deliveredBy(t *testing.T, req *contractv1.DeployRequest, publish func(*fake.Provider)) map[string]string {
	t.Helper()
	builtProject(t)
	provider := fake.NewProvider(fake.Options{})
	client := servedBy(t, provider)
	if publish != nil {
		publish(provider)
	}
	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	plans := provider.Releases().(*fake.Releaser).Plans()
	for i := len(plans) - 1; i >= 0; i-- {
		if plans[i].App != nil {
			return plans[i].App.Values.Delivered
		}
	}
	t.Fatal("no plan the releaser saw stands up an app")
	return nil
}

func TestAResourceIsNamedAsTheRuntimeReadsIt(t *testing.T) {
	t.Parallel()

	if got := providerkit.ResourceEnvName(providerkit.LinkPostgres, "main"); got != "OCEL_RESOURCE_POSTGRES_main" {
		t.Errorf("ResourceEnvName = %q, want the name `ocel dev` and the sdk already agree on", got)
	}
	if got := providerkit.ResourceEnvName(providerkit.LinkBucket, "uploads"); got != "OCEL_RESOURCE_BUCKET_uploads" {
		t.Errorf("ResourceEnvName = %q, want the bucket record read under its resource name", got)
	}
}

func TestAContainerAppRefusesTheNameTheProviderInjects(t *testing.T) {
	message := refusedDeploy(t, declaring(namingARegistry(containerDeployRequest("/healthz")),
		resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, "PORT", "3000"), nil)
	for _, want := range []string{"PORT", "web"} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal reads %q and never names %q", message, want)
		}
	}
}

func TestAServerlessAppIsNotHeldToTheContainerReservation(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := declaring(deployRequest(), resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, "PORT", "3000")
	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("a serverless app declaring PORT = %q, and nothing injects PORT there", result.GetError())
	}
}

func TestEveryValueClassIsDeliveredUnderItsBareNameToAContainer(t *testing.T) {
	req := namingARegistry(containerDeployRequest("/healthz"))
	declaring(req, resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, "REGION", "eu-west-1")
	declaring(req, resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, "API_TOKEN", "sensitive-token")
	declaring(req, resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, "DATABASE_URL", "")

	delivered := deliveredBy(t, req, func(p *fake.Provider) {
		sealValue(t, p, "DATABASE_URL", "postgres://sealed")
	})

	for key, want := range map[string]string{
		"REGION":       "eu-west-1",
		"API_TOKEN":    "sensitive-token",
		"DATABASE_URL": "postgres://sealed",
	} {
		if delivered[key] != want {
			t.Errorf("a container is handed %s=%q, want %q under its bare name", key, delivered[key], want)
		}
	}
	for _, mirrored := range []string{"OCEL_VAR_REGION", "OCEL_VAR_API_TOKEN", "OCEL_VAR_DATABASE_URL"} {
		if _, held := delivered[mirrored]; held {
			t.Errorf("a container is handed %s as well, and one value under two names doubles what an inspect prints", mirrored)
		}
	}
}

func TestALinkRecordIsDeliveredUnderTheNameTheSdkReadsItBy(t *testing.T) {
	req := namingARegistry(containerDeployRequest("/healthz"))
	req.Manifest.Resources = []*contractv1.ManifestResource{{
		LogicalName: "orders",
		Linked:      true,
		Resource:    &resourcesv1.ResourceIdentifier{Type: linksv1.LinkType_LINK_TYPE_POSTGRES, Name: "orders"},
	}}
	req.Manifest.Usages = []*contractv1.ManifestUsage{{App: "web", Resource: "orders"}}

	delivered := deliveredBy(t, req, func(p *fake.Provider) {
		publishRecord(t, p, providerkit.ClassProduction, "terraform", postgresRecord("orders", "terraform"))
	})

	record := delivered[providerkit.ResourceEnvName(providerkit.LinkPostgres, "orders")]
	if record == "" {
		t.Fatalf("a container is handed %v, and nothing under the name the sdk resolves a link by", delivered)
	}
	for _, want := range []string{`"host":"db.example"`, `"password":"hunter2"`, `"port":5432`} {
		if !strings.Contains(strings.ReplaceAll(record, " ", ""), want) {
			t.Errorf("the delivered record reads %q and carries no %s, so a client built from it cannot connect", record, want)
		}
	}
}

func TestAServerlessAppIsHandedNothingResolvedAtDeployTime(t *testing.T) {
	req := declaring(deployRequest(), resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, "DATABASE_URL", "")
	delivered := deliveredBy(t, req, func(p *fake.Provider) {
		sealValue(t, p, "DATABASE_URL", "postgres://sealed")
	})
	if len(delivered) != 0 {
		t.Errorf("a serverless app is handed %v at deploy time, and a secret's plaintext never enters its configuration", delivered)
	}
}
