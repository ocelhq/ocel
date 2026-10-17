package providerserver_test

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
	"google.golang.org/protobuf/types/known/structpb"
)

func bindingRequest(name string, kind bindingsv1.BindingType) *contractv1.DeployRequest {
	return externalBindingRequest(name, name, kind)
}

func externalBindingRequest(name, external string, kind bindingsv1.BindingType) *contractv1.DeployRequest {
	req := deployRequest()
	req.Manifest.Resources = []*contractv1.ManifestResource{{
		LogicalName: name,
		Binding:     external,
		Resource:    &resourcesv1.ResourceIdentifier{Type: declaredAs(kind), Name: name},
	}}
	return req
}

func declaredAs(kind bindingsv1.BindingType) resourcesv1.ResourceType {
	for _, declared := range naming.BindableResourceTypes() {
		if bound, _ := naming.BindableAs(declared); bound == kind {
			return declared
		}
	}
	return resourcesv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED
}

func publishRecord(t *testing.T, vendor *fake.Provider, tier environment.Tier, owner string, binding *bindingsv1.Binding) {
	t.Helper()
	pair, err := variablestoreserver.BindingPair(owner, binding)
	if err != nil {
		t.Fatalf("BindingPair: %v", err)
	}
	store := variablestore.Store{KeyValues: vendor.KeyValues(), Cipher: vendor.Cipher()}
	scope := variablestore.Scope{Project: "shop", Tier: tier}
	if _, err := store.SetBindings(context.Background(), scope, "", owner, []variablestore.NamedBindingWrite{{Name: binding.GetName(), Write: pair}}); err != nil {
		t.Fatalf("SetBindings: %v", err)
	}
}

func postgresRecord(name, source string) *bindingsv1.Binding {
	return &bindingsv1.Binding{
		Name:   name,
		Source: source,
		Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
			Host: "db.example", Port: 5432, Database: "orders", Username: "app", Password: "hunter2",
		}},
	}
}

func bucketRecord(name, source string) *bindingsv1.Binding {
	return &bindingsv1.Binding{
		Name:       name,
		Source:     source,
		Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: "uploads-1"}},
	}
}

func refusedDeploy(t *testing.T, req *contractv1.DeployRequest, publish func(*fake.Provider)) string {
	t.Helper()
	builtProject(t)
	client, vendor := deployServed(t)
	if publish != nil {
		publish(vendor)
	}
	stream, err := client.Deploy(context.Background(), req)
	if err != nil {
		t.Fatalf("Deploy() error = %v", err)
	}
	defer stream.Close()
	refusal := ""
	for stream.Receive() {
		result := stream.Msg().GetResult()
		if result.GetSuccess() {
			t.Fatal("Deploy() succeeded, want the binding refused before anything was provisioned")
		}
		if result.GetError() != "" {
			refusal = result.GetError()
		}
	}
	if err := stream.Err(); err != nil {
		refusal = err.Error()
	}
	if refusal == "" {
		t.Fatal("Deploy() ended with no refusal, want the binding refused")
	}
	return refusal
}

func TestDeployRefusesABindingNothingPublished(t *testing.T) {
	t.Run("names the binding and the coordinate it looked in", func(t *testing.T) {
		message := refusedDeploy(t, bindingRequest("orders", bindingsv1.BindingType_BINDING_TYPE_POSTGRES), nil)
		for _, want := range []string{"orders", "prod", "Nothing at all is published"} {
			if !strings.Contains(message, want) {
				t.Errorf("refusal = %q, want it to contain %q", message, want)
			}
		}
	})

	t.Run("points at the tier the record was published to instead", func(t *testing.T) {
		message := refusedDeploy(t, bindingRequest("orders", bindingsv1.BindingType_BINDING_TYPE_POSTGRES), func(p *fake.Provider) {
			publishRecord(t, p, environment.TierPreview, "terraform", postgresRecord("orders", "terraform"))
		})
		if !strings.Contains(message, string(environment.TierPreview)) {
			t.Errorf("refusal = %q, want it to name the tier publishing the record", message)
		}
	})
}

func TestDeployRefusesABindingTheRecordCannotSatisfy(t *testing.T) {
	t.Run("a shape the app would cold-start against", func(t *testing.T) {
		message := refusedDeploy(t, bindingRequest("orders", bindingsv1.BindingType_BINDING_TYPE_POSTGRES), func(p *fake.Provider) {
			publishRecord(t, p, environment.TierProduction, "terraform", bucketRecord("orders", "terraform"))
		})
		if !strings.Contains(message, "cold start") {
			t.Errorf("refusal = %q, want the shape mismatch refused", message)
		}
	})

	t.Run("a bucket ocel's client did not provision", func(t *testing.T) {
		message := refusedDeploy(t, bindingRequest("uploads", bindingsv1.BindingType_BINDING_TYPE_BUCKET), func(p *fake.Provider) {
			publishRecord(t, p, environment.TierProduction, "terraform", bucketRecord("uploads", "terraform"))
		})
		if !strings.Contains(message, "terraform") {
			t.Errorf("refusal = %q, want it to name the publisher ocel cannot serve for", message)
		}
	})

	t.Run("a postgres record a publisher of yours owns is admitted", func(t *testing.T) {
		builtProject(t)
		client, vendor := deployServed(t)
		publishRecord(t, vendor, environment.TierProduction, "terraform", postgresRecord("orders", "terraform"))

		result, _ := deploy(t, client, bindingRequest("orders", bindingsv1.BindingType_BINDING_TYPE_POSTGRES))
		if !result.GetSuccess() {
			t.Fatalf("Deploy() = %q, want a foreign postgres record bound", result.GetError())
		}
	})
}

func TestRefuseMismatchedBinding(t *testing.T) {
	t.Run("refuses a custom record bound as a binding", func(t *testing.T) {
		err := providerserver.RefuseMismatchedBinding(provider.Binding{Name: "flags", Type: provider.BindingCustom}, "settings", provider.BindingPostgres)
		if err == nil || !strings.Contains(err.Error(), "`bindings.custom.flags.<property>`") {
			t.Errorf("RefuseMismatchedBinding = %v, want a custom record sent to transforms by the key a transform reads it under", err)
		}
	})

	t.Run("admits a record of the declared type ocel provisioned", func(t *testing.T) {
		if err := providerserver.RefuseMismatchedBinding(provider.Binding{Name: "uploads", Type: provider.BindingBucket}, "uploads", provider.BindingBucket); err != nil {
			t.Errorf("RefuseMismatchedBinding = %v, want a record ocel published bound", err)
		}
	})

	t.Run("refuses a published bucket the runtime has no store to reach it in", func(t *testing.T) {
		published := provider.Binding{Name: "uploads", Type: provider.BindingBucket, Source: "terraform", Properties: map[string]string{provider.PropertyBucket: "acme"}}
		if err := providerserver.RefuseMismatchedBinding(published, "uploads", provider.BindingBucket); err == nil {
			t.Error("RefuseMismatchedBinding = nil, want a bucket ocel's backend cannot reach refused")
		}
	})

	t.Run("admits a bucket record that names the store it lives in", func(t *testing.T) {
		bound := provider.Binding{Name: "ocel:bucket.uploads", Type: provider.BindingBucket, Source: "ocel.json", Properties: map[string]string{
			provider.PropertyBucket: "acme", provider.PropertyEndpoint: "https://abc.r2.cloudflarestorage.com",
		}}
		if err := providerserver.RefuseMismatchedBinding(bound, "uploads", provider.BindingBucket); err != nil {
			t.Errorf("RefuseMismatchedBinding = %v, want a bucket the runtime serves from its record admitted", err)
		}
	})

	t.Run("admits a published task or topic record, which names no store", func(t *testing.T) {
		for _, kind := range []provider.BindingType{provider.BindingTask, provider.BindingTopic} {
			published := provider.Binding{Name: "orders", Type: kind, Source: "terraform"}
			if err := providerserver.RefuseMismatchedBinding(published, "orders", kind); err != nil {
				t.Errorf("RefuseMismatchedBinding = %v, want a published %s admitted: only a bucket is reached in a store its record names", err, kind)
			}
		}
	})

	t.Run("a shape mismatch names the declared name and the external name apart", func(t *testing.T) {
		err := providerserver.RefuseMismatchedBinding(provider.Binding{Name: "sst-pg-orders", Type: provider.BindingBucket}, "orders", provider.BindingPostgres)
		if err == nil {
			t.Fatal("RefuseMismatchedBinding = nil, want a bucket refused where a postgres was declared")
		}
		for _, want := range []string{"orders", "sst-pg-orders"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("RefuseMismatchedBinding = %v, want it to name %q — the two ids differ and only one of them is greppable in the app", err, want)
			}
		}
	})
}

func TestDeployBindsByTheExternalName(t *testing.T) {
	t.Run("a record published under a name the app never declares is admitted", func(t *testing.T) {
		builtProject(t)
		client, vendor := deployServed(t)
		publishRecord(t, vendor, environment.TierProduction, "terraform", postgresRecord("sst-pg-orders", "terraform"))

		result, _ := deploy(t, client, externalBindingRequest("orders", "sst-pg-orders", bindingsv1.BindingType_BINDING_TYPE_POSTGRES))
		if !result.GetSuccess() {
			t.Fatalf("Deploy() = %q, want the record found under the external name", result.GetError())
		}
	})

	t.Run("the declared name alone does not satisfy a binding", func(t *testing.T) {
		message := refusedDeploy(t, externalBindingRequest("orders", "sst-pg-orders", bindingsv1.BindingType_BINDING_TYPE_POSTGRES), func(p *fake.Provider) {
			publishRecord(t, p, environment.TierProduction, "terraform", postgresRecord("orders", "terraform"))
		})
		if !strings.Contains(message, "sst-pg-orders") {
			t.Errorf("refusal = %q, want it to name the external name it looked for", message)
		}
	})
}

func TestDeployDeliversABoundRecordUnderTheNameTheAppDeclares(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	publishRecord(t, vendor, environment.TierProduction, "terraform", postgresRecord("shared-db", "terraform"))

	req := externalBindingRequest("main", "shared-db", bindingsv1.BindingType_BINDING_TYPE_POSTGRES)
	req.Manifest.Usages = []*contractv1.ManifestUsage{{App: "web", Resource: "main"}}
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	var delivered []provider.Binding
	for _, spec := range vendor.FakeStacks().Provisioned() {
		if spec.App != nil && spec.App.App == "web" {
			delivered = spec.App.Values.Bindings
		}
	}
	if len(delivered) != 1 {
		t.Fatalf("web is handed %+v, want the one record it binds", delivered)
	}
	binding := delivered[0]
	if key := provider.ResourceEnvName(binding.Type, binding.Resource); key != "OCEL_RESOURCE_POSTGRES_main" {
		t.Errorf("web is handed the record under %q, want OCEL_RESOURCE_POSTGRES_main, the key postgres(\"main\") reads", key)
	}
	if binding.Name != "shared-db" || binding.Properties[provider.PropertyHost] != "db.example" {
		t.Errorf("web is handed %+v, want the record published as shared-db", binding)
	}
}

func TestDeployDeliversOneRecordUnderEachNameThatBindsIt(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	publishRecord(t, vendor, environment.TierProduction, "terraform", postgresRecord("shared-db", "terraform"))

	req := externalBindingRequest("main", "shared-db", bindingsv1.BindingType_BINDING_TYPE_POSTGRES)
	req.Manifest.Resources = append(req.Manifest.Resources, &contractv1.ManifestResource{
		LogicalName: "reports",
		Binding:     "shared-db",
		Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "reports"},
	})
	req.Manifest.Usages = []*contractv1.ManifestUsage{{App: "web", Resource: "main"}, {App: "web", Resource: "reports"}}
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	var keys []string
	for _, spec := range vendor.FakeStacks().Provisioned() {
		if spec.App == nil || spec.App.App != "web" {
			continue
		}
		for _, binding := range spec.App.Values.Bindings {
			keys = append(keys, provider.ResourceEnvName(binding.Type, binding.Resource))
		}
	}
	if want := []string{"OCEL_RESOURCE_POSTGRES_main", "OCEL_RESOURCE_POSTGRES_reports"}; !slices.Equal(keys, want) {
		t.Errorf("web is handed shared-db under %v, want %v: postgres(\"main\") and postgres(\"reports\") each read their own key", keys, want)
	}
}

func TestDeployDeliversARecordTheProjectPublishesUnderEachNameThatBindsIt(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	req := deployRequest()
	req.Manifest.Resources = append(req.Manifest.Resources, &contractv1.ManifestResource{
		LogicalName: "main",
		Binding:     "orders",
		Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, Name: "main"},
	})
	req.Manifest.Usages = append(req.Manifest.Usages, &contractv1.ManifestUsage{App: "web", Resource: "main"})
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	var keys []string
	for _, spec := range vendor.FakeStacks().Provisioned() {
		if spec.App == nil || spec.App.App != "web" {
			continue
		}
		keys = keys[:0]
		for _, binding := range spec.App.Values.Bindings {
			keys = append(keys, provider.ResourceEnvName(binding.Type, cmp.Or(binding.Resource, binding.Name)))
		}
	}
	slices.Sort(keys)
	if want := []string{"OCEL_RESOURCE_POSTGRES_main", "OCEL_RESOURCE_POSTGRES_orders"}; !slices.Equal(keys, want) {
		t.Errorf("the second deploy hands web orders under %v, want %v: postgres(\"orders\") and postgres(\"main\") each read their own key", keys, want)
	}
}

func TestADryRunAdmitsTheRecordAnInlineBindingWritesOnlyAtDeploy(t *testing.T) {
	t.Run("a dry run plans with the record the request carries", func(t *testing.T) {
		builtProject(t)
		client, _ := deployServed(t)
		req := inlinePostgresRequest(servePostgres(t, "170004", 0).url(), "17")
		req.Dry = true

		result, _ := deploy(t, client, req)
		if !result.GetSuccess() {
			t.Fatalf("Deploy(dry) = %q, want the plan made: the record is written by the deploy the plan previews", result.GetError())
		}
	})

	t.Run("a deploy still refuses it missing", func(t *testing.T) {
		req := externalBindingRequest("orders", naming.InlineRecordName(resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, "orders"), bindingsv1.BindingType_BINDING_TYPE_POSTGRES)
		message := refusedDeploy(t, req, nil)
		if !strings.Contains(message, "ocel:postgres.orders") {
			t.Errorf("refusal = %q, want the record named", message)
		}
	})
}

func TestAProductionResourceNamedLikeAnotherPublishersRecordIsRefusedBeforeAnythingIsProvisioned(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	publishRecord(t, vendor, environment.TierProduction, "terraform", postgresRecord("orders", "terraform"))

	said := expectDeployRefused(t, client, externalBindingRequest("orders", "", bindingsv1.BindingType_BINDING_TYPE_POSTGRES))

	if !strings.Contains(said, "publisher terraform") {
		t.Errorf("Deploy() said %q, want it to name the publisher that holds orders", said)
	}
	if got := len(vendor.FakeStacks().Provisioned()); got != 0 {
		t.Errorf("the refused deploy provisioned %d stacks, want none: publishing orders afterwards is refused, so its infra would be left behind", got)
	}
}

func TestDeployWarnsWhenItProvisionsBesideAPublishedNamesake(t *testing.T) {
	warnings := func(t *testing.T, record *bindingsv1.Binding, kind bindingsv1.BindingType) []string {
		t.Helper()
		builtProject(t)
		client, vendor := deployServed(t)
		publishRecord(t, vendor, environment.TierProduction, "terraform", record)
		req := externalBindingRequest(record.GetName(), "", kind)
		req.Manifest.Resources[0].LogicalName = "db--" + record.GetName()
		result, events := deploy(t, client, req)
		if !result.GetSuccess() {
			t.Fatalf("Deploy() = %q, want the resource provisioned beside the record", result.GetError())
		}
		var messages []string
		for _, event := range events {
			if event.GetBody() == nil && event.GetLevel() == progressv1.Level_LEVEL_WARN {
				messages = append(messages, event.GetMessage())
			}
		}
		return messages
	}
	warned := func(messages []string) bool {
		return slices.ContainsFunc(messages, func(message string) bool { return strings.Contains(message, "`bindings`") })
	}

	t.Run("shows the binding written as config accepts it, beside a record of the same type", func(t *testing.T) {
		want := `"bindings": { "postgres": { "orders": "@orders" } }`
		messages := warnings(t, postgresRecord("orders", "terraform"), bindingsv1.BindingType_BINDING_TYPE_POSTGRES)
		if !slices.ContainsFunc(messages, func(message string) bool { return strings.Contains(message, want) }) {
			t.Errorf("no warning contains %s: the warning must show the binding written as config accepts it", want)
		}
	})

	t.Run("says nothing beside a custom record, which no binding can consume", func(t *testing.T) {
		custom, err := structpb.NewStruct(map[string]any{"region": "eu-west-1"})
		if err != nil {
			t.Fatal(err)
		}
		messages := warnings(t, &bindingsv1.Binding{Name: "orders", Source: "terraform", Properties: &bindingsv1.Binding_Custom{Custom: custom}}, bindingsv1.BindingType_BINDING_TYPE_POSTGRES)
		if warned(messages) {
			t.Errorf("progress = %q, want no advice to bind a custom record: the binding it suggests is refused", messages)
		}
	})

	t.Run("says nothing beside a record of another type", func(t *testing.T) {
		messages := warnings(t, bucketRecord("orders", "terraform"), bindingsv1.BindingType_BINDING_TYPE_POSTGRES)
		if warned(messages) {
			t.Errorf("progress = %q, want no advice to bind a bucket as a postgres: the binding it suggests is refused", messages)
		}
	})

	t.Run("says nothing beside a record ocel's client cannot serve", func(t *testing.T) {
		messages := warnings(t, bucketRecord("uploads", "terraform"), bindingsv1.BindingType_BINDING_TYPE_BUCKET)
		if warned(messages) {
			t.Errorf("progress = %q, want no advice to bind a bucket another publisher provisioned: the binding it suggests is refused", messages)
		}
	})
}

func TestDeployRefusesAVariableClassItCannotDeliver(t *testing.T) {
	req := deployRequest()
	req.Manifest.Apps[0].Variables = []*contractv1.ManifestVariable{
		{Key: "WEBHOOK_SECRET", Value: "whsec", Class: resourcesv1.VariableClass_VARIABLE_CLASS_UNSPECIFIED},
	}
	message := refusedDeploy(t, req, nil)
	for _, want := range []string{"web", "WEBHOOK_SECRET"} {
		if !strings.Contains(message, want) {
			t.Errorf("refusal = %q, want it to name %q", message, want)
		}
	}
}
