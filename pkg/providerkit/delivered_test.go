package providerkit_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
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

func refusedPlan(t *testing.T, req *contractv1.DeployRequest) (string, []*progressv1.OperationEvent) {
	t.Helper()
	builtProject(t)
	client, _ := deployServed(t)
	req.Dry = true
	var events []*progressv1.OperationEvent
	stream, err := client.Deploy(context.Background(), req)
	if err != nil {
		t.Fatalf("Deploy() error = %v", err)
	}
	defer stream.Close()
	refusal := ""
	for stream.Receive() {
		events = append(events, stream.Msg())
		result := stream.Msg().GetResult()
		if result.GetSuccess() {
			t.Fatal("a plan of it succeeded, want it refused before the deploy that would carry it out is ever run")
		}
		if result.GetError() != "" {
			refusal = result.GetError()
		}
	}
	if err := stream.Err(); err != nil {
		refusal = err.Error()
	}
	if refusal == "" {
		t.Fatal("a plan of it ended with no refusal at all")
	}
	return refusal, events
}

func entered(t *testing.T, events []*progressv1.OperationEvent, app string) bool {
	t.Helper()
	for _, event := range events {
		if event.GetSpan().GetName() == app {
			return true
		}
	}
	return false
}

func TestAContainerAppsReservedNamesAreRefusedByThePlanRatherThanByTheDeploy(t *testing.T) {
	for what, declaring := range map[string]func(*contractv1.DeployRequest) *contractv1.DeployRequest{
		"the port a provider injects": func(req *contractv1.DeployRequest) *contractv1.DeployRequest {
			return declaring(req, resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, "PORT", "3000")
		},
		"an ocel-owned name as a plain value": func(req *contractv1.DeployRequest) *contractv1.DeployRequest {
			return declaring(req, resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, "OCEL_VAR_X", "1")
		},
		"an ocel-owned name as a sensitive value": func(req *contractv1.DeployRequest) *contractv1.DeployRequest {
			return declaring(req, resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, "OCEL_LIVE_KEYS", "1")
		},
		"an ocel-owned name as a secret": func(req *contractv1.DeployRequest) *contractv1.DeployRequest {
			return declaring(req, resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, "OCEL_RESOURCE_POSTGRES_orders", "")
		},
	} {
		t.Run(what, func(t *testing.T) {
			message, events := refusedPlan(t, declaring(namingARegistry(containerDeployRequest("/healthz"))))
			if !strings.Contains(message, "web") {
				t.Errorf("the refusal reads %q and never names the app that declares it", message)
			}
			if entered(t, events, "web") {
				t.Error("the deploy was already standing web up when the name was refused: settling on a plan is where a name a provider owns is read, and a refusal that waits for the app's own unit is one a user meets only once the deploy is under way")
			}
		})
	}
}

func TestAnUnsetSecretIsRefusedByThePlanOfAContainerApp(t *testing.T) {
	message, events := refusedPlan(t, declaring(namingARegistry(containerDeployRequest("/healthz")),
		resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, "DATABASE_URL", ""))
	for _, want := range []string{"web", "DATABASE_URL", "ocel env set"} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal reads %q and never names %q", message, want)
		}
	}
	if entered(t, events, "web") {
		t.Error("the deploy was already standing web up when the unset secret was refused")
	}
}

func TestAServerlessAppIsHeldToNeitherReservation(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := declaring(deployRequest(), resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, "OCEL_ROTATION_TOKEN", "")
	declaring(req, resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, "PORT", "3000")
	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("a serverless app declaring both = %q: nothing is delivered bare there, so neither name is taken", result.GetError())
	}
}

func TestALinkThisDeployProvisionsIsDeliveredAsItsRecordRatherThanAsNothing(t *testing.T) {
	delivered := deliveredBy(t, namingARegistry(containerDeployRequest("/healthz")), nil)

	name := providerkit.ResourceEnvName(providerkit.LinkPostgres, "orders")
	record := delivered[name]
	if record == "" {
		t.Fatalf("a container is handed %s=%q for a resource this very deploy provisioned, and an app reading it builds a client out of an empty string", name, record)
	}
	if !strings.Contains(record, `"orders"`) {
		t.Errorf("the delivered record reads %q and does not name the resource it stands for", record)
	}
}

func TestTheStagedRecordNamesEveryValueTheAppDeclaresAndCarriesNone(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	held := staging(t, provider)

	req := namingARegistry(containerDeployRequest("/healthz"))
	declaring(req, resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, "REGION", "eu-west-1")
	declaring(req, resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, "API_TOKEN", "sensitive-token")
	declaring(req, resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, "DATABASE_URL", "")
	sealValue(t, provider, "DATABASE_URL", "postgres://sealed")

	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	staged := held.records()
	if len(staged) != 1 {
		t.Fatalf("the deploy staged %d records, want the one app it released", len(staged))
	}
	var named []string
	for _, variable := range staged[0].Variables {
		named = append(named, variable.Key)
	}
	if !slices.Equal(named, []string{"API_TOKEN", "DATABASE_URL", "REGION"}) {
		t.Errorf("the staged record names %v of what web declares, and a promotion reads that record to decide whether putting the app back would serve it an empty environment", named)
	}
	rendered := fmt.Sprint(staged[0])
	for _, held := range []string{"eu-west-1", "sensitive-token", "postgres://sealed"} {
		if strings.Contains(rendered, held) {
			t.Errorf("the staged record reads %q and carries a value in plaintext: a record outlives the deploy that wrote it, and the names alone are what a promotion needs", rendered)
		}
	}
}
