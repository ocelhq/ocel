package providerserver_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/envvars"
	"github.com/ocelhq/ocel/pkg/providerkit/envvarsserver"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/providerserver"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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

func publishRecord(t *testing.T, provider *fake.Provider, class edge.Class, owner string, binding *bindingsv1.Binding) {
	t.Helper()
	pair, err := envvarsserver.BindingPair(owner, binding)
	if err != nil {
		t.Fatalf("BindingPair: %v", err)
	}
	store := envvars.Store{Records: provider.Records(), Cipher: provider.Cipher()}
	scope := envvars.Scope{Project: "shop", Class: class}
	if _, err := store.SetBindings(context.Background(), scope, "", owner, []envvars.NamedBindingWrite{{Name: binding.GetName(), Write: pair}}); err != nil {
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
	client, provider := deployServed(t)
	if publish != nil {
		publish(provider)
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
			t.Fatal("Deploy() succeeded, want the binding refused before anything was stood up")
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
				t.Errorf("refusal = %q, want it to carry %q", message, want)
			}
		}
	})

	t.Run("points at the class the record was published to instead", func(t *testing.T) {
		message := refusedDeploy(t, bindingRequest("orders", bindingsv1.BindingType_BINDING_TYPE_POSTGRES), func(p *fake.Provider) {
			publishRecord(t, p, edge.ClassPreview, "terraform", postgresRecord("orders", "terraform"))
		})
		if !strings.Contains(message, string(edge.ClassPreview)) {
			t.Errorf("refusal = %q, want it to name the class publishing the record", message)
		}
	})
}

func TestDeployRefusesABindingTheRecordCannotSatisfy(t *testing.T) {
	t.Run("a shape the app would cold-start against", func(t *testing.T) {
		message := refusedDeploy(t, bindingRequest("orders", bindingsv1.BindingType_BINDING_TYPE_POSTGRES), func(p *fake.Provider) {
			publishRecord(t, p, edge.ClassProduction, "terraform", bucketRecord("orders", "terraform"))
		})
		if !strings.Contains(message, "cold start") {
			t.Errorf("refusal = %q, want the shape mismatch refused", message)
		}
	})

	t.Run("a bucket ocel's client did not provision", func(t *testing.T) {
		message := refusedDeploy(t, bindingRequest("uploads", bindingsv1.BindingType_BINDING_TYPE_BUCKET), func(p *fake.Provider) {
			publishRecord(t, p, edge.ClassProduction, "terraform", bucketRecord("uploads", "terraform"))
		})
		if !strings.Contains(message, "terraform") {
			t.Errorf("refusal = %q, want it to name the publisher ocel cannot serve for", message)
		}
	})

	t.Run("a postgres record a publisher of yours owns is admitted", func(t *testing.T) {
		builtProject(t)
		client, provider := deployServed(t)
		publishRecord(t, provider, edge.ClassProduction, "terraform", postgresRecord("orders", "terraform"))

		result, _ := deploy(t, client, bindingRequest("orders", bindingsv1.BindingType_BINDING_TYPE_POSTGRES))
		if !result.GetSuccess() {
			t.Fatalf("Deploy() = %q, want a foreign postgres record bound", result.GetError())
		}
	})
}

func TestRefuseMismatchedBinding(t *testing.T) {
	t.Run("refuses a custom record bound as a binding", func(t *testing.T) {
		err := providerserver.RefuseMismatchedBinding(provider.Binding{Name: "flags", Type: provider.BindingCustom}, "settings", provider.BindingPostgres, proxied)
		if err == nil || !strings.Contains(err.Error(), "`bindings.custom.flags.<property>`") {
			t.Errorf("RefuseMismatchedBinding = %v, want a custom record sent to transforms by the key a transform reads it under", err)
		}
	})

	t.Run("admits a record of the declared type ocel provisioned", func(t *testing.T) {
		if err := providerserver.RefuseMismatchedBinding(provider.Binding{Name: "uploads", Type: provider.BindingBucket}, "uploads", provider.BindingBucket, proxied); err != nil {
			t.Errorf("RefuseMismatchedBinding = %v, want a record ocel published bound", err)
		}
	})

	t.Run("refuses a published bucket the runtime has no store to reach it in", func(t *testing.T) {
		published := provider.Binding{Name: "uploads", Type: provider.BindingBucket, Source: "terraform", Properties: map[string]string{provider.PropertyBucket: "acme"}}
		if err := providerserver.RefuseMismatchedBinding(published, "uploads", provider.BindingBucket, proxied); err == nil {
			t.Error("RefuseMismatchedBinding = nil, want a bucket ocel's backend cannot reach refused")
		}
	})

	t.Run("admits a bucket record that names the store it lives in", func(t *testing.T) {
		bound := provider.Binding{Name: "ocel:bucket.uploads", Type: provider.BindingBucket, Source: "ocel.json", Properties: map[string]string{
			provider.PropertyBucket: "acme", provider.PropertyEndpoint: "https://abc.r2.cloudflarestorage.com",
		}}
		if err := providerserver.RefuseMismatchedBinding(bound, "uploads", provider.BindingBucket, proxied); err != nil {
			t.Errorf("RefuseMismatchedBinding = %v, want a bucket the runtime serves from its record admitted", err)
		}
	})

	t.Run("a shape mismatch names the declared name and the external name apart", func(t *testing.T) {
		err := providerserver.RefuseMismatchedBinding(provider.Binding{Name: "sst-pg-orders", Type: provider.BindingBucket}, "orders", provider.BindingPostgres, proxied)
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
		client, provider := deployServed(t)
		publishRecord(t, provider, edge.ClassProduction, "terraform", postgresRecord("sst-pg-orders", "terraform"))

		result, _ := deploy(t, client, externalBindingRequest("orders", "sst-pg-orders", bindingsv1.BindingType_BINDING_TYPE_POSTGRES))
		if !result.GetSuccess() {
			t.Fatalf("Deploy() = %q, want the record found under the external name", result.GetError())
		}
	})

	t.Run("the declared name alone does not satisfy a binding", func(t *testing.T) {
		message := refusedDeploy(t, externalBindingRequest("orders", "sst-pg-orders", bindingsv1.BindingType_BINDING_TYPE_POSTGRES), func(p *fake.Provider) {
			publishRecord(t, p, edge.ClassProduction, "terraform", postgresRecord("orders", "terraform"))
		})
		if !strings.Contains(message, "sst-pg-orders") {
			t.Errorf("refusal = %q, want it to name the external name it looked for", message)
		}
	})
}

func TestADryRunAdmitsTheRecordAnInlineBindingWritesOnlyAtDeploy(t *testing.T) {
	t.Run("a dry run plans without it", func(t *testing.T) {
		builtProject(t)
		client, _ := deployServed(t)
		req := externalBindingRequest("orders", naming.InlineRecordName(resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES, "orders"), bindingsv1.BindingType_BINDING_TYPE_POSTGRES)
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

func TestDeployWarnsWhenItProvisionsBesideAPublishedNamesake(t *testing.T) {
	said := func(t *testing.T, record *bindingsv1.Binding, kind bindingsv1.BindingType) []string {
		t.Helper()
		builtProject(t)
		client, provider := deployServed(t)
		publishRecord(t, provider, edge.ClassProduction, "terraform", record)
		req := externalBindingRequest(record.GetName(), "", kind)
		req.Manifest.Resources[0].LogicalName = "db--" + record.GetName()
		result, events := deploy(t, client, req)
		if !result.GetSuccess() {
			t.Fatalf("Deploy() = %q, want the resource provisioned beside the record", result.GetError())
		}
		var messages []string
		for _, event := range events {
			messages = append(messages, event.GetProgress().GetMessage())
		}
		return messages
	}
	warned := func(messages []string) bool {
		return slices.ContainsFunc(messages, func(message string) bool { return strings.Contains(message, "`bindings`") })
	}

	t.Run("shows the binding written as config accepts it, beside a record of the same type", func(t *testing.T) {
		want := `"bindings": { "postgres": { "orders": "@orders" } }`
		messages := said(t, postgresRecord("orders", "terraform"), bindingsv1.BindingType_BINDING_TYPE_POSTGRES)
		if !slices.ContainsFunc(messages, func(message string) bool { return strings.Contains(message, want) }) {
			t.Errorf("no progress message carries %s: the warning must show the binding written as config accepts it", want)
		}
	})

	t.Run("says nothing beside a custom record, which no binding can consume", func(t *testing.T) {
		custom, err := structpb.NewStruct(map[string]any{"region": "eu-west-1"})
		if err != nil {
			t.Fatal(err)
		}
		messages := said(t, &bindingsv1.Binding{Name: "orders", Source: "terraform", Properties: &bindingsv1.Binding_Custom{Custom: custom}}, bindingsv1.BindingType_BINDING_TYPE_POSTGRES)
		if warned(messages) {
			t.Errorf("progress = %q, want no advice to bind a custom record: the binding it suggests is refused", messages)
		}
	})

	t.Run("says nothing beside a record of another type", func(t *testing.T) {
		messages := said(t, bucketRecord("orders", "terraform"), bindingsv1.BindingType_BINDING_TYPE_POSTGRES)
		if warned(messages) {
			t.Errorf("progress = %q, want no advice to bind a bucket as a postgres: the binding it suggests is refused", messages)
		}
	})

	t.Run("says nothing beside a record ocel's client cannot serve", func(t *testing.T) {
		messages := said(t, bucketRecord("uploads", "terraform"), bindingsv1.BindingType_BINDING_TYPE_BUCKET)
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
