package providerkit_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
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

func publishRecord(t *testing.T, provider *fake.Provider, class providerkit.Class, owner string, binding *bindingsv1.Binding) {
	t.Helper()
	pair, err := providerkit.BindingPair(owner, binding)
	if err != nil {
		t.Fatalf("BindingPair: %v", err)
	}
	store := values.Store{Records: provider.Records(), Sealer: provider.Sealer()}
	scope := values.Scope{Project: "shop", Class: class}
	if _, err := store.SetBindings(context.Background(), scope, "", owner, []values.Publishing{{Name: binding.GetName(), Pair: pair}}); err != nil {
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
			publishRecord(t, p, providerkit.ClassPreview, "terraform", postgresRecord("orders", "terraform"))
		})
		if !strings.Contains(message, string(providerkit.ClassPreview)) {
			t.Errorf("refusal = %q, want it to name the class publishing the record", message)
		}
	})
}

func TestDeployRefusesABindingTheRecordCannotSatisfy(t *testing.T) {
	t.Run("a shape the app would cold-start against", func(t *testing.T) {
		message := refusedDeploy(t, bindingRequest("orders", bindingsv1.BindingType_BINDING_TYPE_POSTGRES), func(p *fake.Provider) {
			publishRecord(t, p, providerkit.ClassProduction, "terraform", bucketRecord("orders", "terraform"))
		})
		if !strings.Contains(message, "cold start") {
			t.Errorf("refusal = %q, want the shape mismatch refused", message)
		}
	})

	t.Run("a bucket ocel's client did not provision", func(t *testing.T) {
		message := refusedDeploy(t, bindingRequest("uploads", bindingsv1.BindingType_BINDING_TYPE_BUCKET), func(p *fake.Provider) {
			publishRecord(t, p, providerkit.ClassProduction, "terraform", bucketRecord("uploads", "terraform"))
		})
		if !strings.Contains(message, "terraform") {
			t.Errorf("refusal = %q, want it to name the publisher ocel cannot serve for", message)
		}
	})

	t.Run("a postgres record a publisher of yours owns is admitted", func(t *testing.T) {
		builtProject(t)
		client, provider := deployServed(t)
		publishRecord(t, provider, providerkit.ClassProduction, "terraform", postgresRecord("orders", "terraform"))

		result, _ := deploy(t, client, bindingRequest("orders", bindingsv1.BindingType_BINDING_TYPE_POSTGRES))
		if !result.GetSuccess() {
			t.Fatalf("Deploy() = %q, want a foreign postgres record bound", result.GetError())
		}
	})
}

type servingBuckets struct{ *fake.Provider }

func (servingBuckets) Proxied(providerkit.BindingType) bool { return false }

func TestAVendorSaysWhichBindingTypesItsAppsReachThroughTheRuntime(t *testing.T) {
	builtProject(t)
	base := fake.NewProvider(fake.Options{})
	client := servedBy(t, servingBuckets{Provider: base})
	publishRecord(t, base, providerkit.ClassProduction, "terraform", bucketRecord("uploads", "terraform"))

	result, _ := deploy(t, client, bindingRequest("uploads", bindingsv1.BindingType_BINDING_TYPE_BUCKET))
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want a foreign bucket bound: this vendor's runtime serves one it did not provision", result.GetError())
	}
}

func TestReadableAs(t *testing.T) {
	t.Run("refuses a custom record bound as a binding", func(t *testing.T) {
		err := providerkit.ReadableAs(providerkit.Binding{Name: "flags", Type: providerkit.BindingCustom}, "flags", providerkit.BindingPostgres, providerkit.Proxied)
		if err == nil || !strings.Contains(err.Error(), "transform") {
			t.Errorf("ReadableAs = %v, want a custom record sent to transforms", err)
		}
	})

	t.Run("admits a record of the declared type ocel provisioned", func(t *testing.T) {
		if err := providerkit.ReadableAs(providerkit.Binding{Name: "uploads", Type: providerkit.BindingBucket}, "uploads", providerkit.BindingBucket, providerkit.Proxied); err != nil {
			t.Errorf("ReadableAs = %v, want a record ocel published bound", err)
		}
	})

	t.Run("a shape mismatch names the declared name and the external name apart", func(t *testing.T) {
		err := providerkit.ReadableAs(providerkit.Binding{Name: "sst-pg-orders", Type: providerkit.BindingBucket}, "orders", providerkit.BindingPostgres, providerkit.Proxied)
		if err == nil {
			t.Fatal("ReadableAs = nil, want a bucket refused where a postgres was declared")
		}
		for _, want := range []string{"orders", "sst-pg-orders"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("ReadableAs = %v, want it to name %q — the two ids differ and only one of them is greppable in the app", err, want)
			}
		}
	})
}

func TestDeployBindsByTheExternalName(t *testing.T) {
	t.Run("a record published under a name the app never declares is admitted", func(t *testing.T) {
		builtProject(t)
		client, provider := deployServed(t)
		publishRecord(t, provider, providerkit.ClassProduction, "terraform", postgresRecord("sst-pg-orders", "terraform"))

		result, _ := deploy(t, client, externalBindingRequest("orders", "sst-pg-orders", bindingsv1.BindingType_BINDING_TYPE_POSTGRES))
		if !result.GetSuccess() {
			t.Fatalf("Deploy() = %q, want the record found under the external name", result.GetError())
		}
	})

	t.Run("the declared name alone does not satisfy a binding", func(t *testing.T) {
		message := refusedDeploy(t, externalBindingRequest("orders", "sst-pg-orders", bindingsv1.BindingType_BINDING_TYPE_POSTGRES), func(p *fake.Provider) {
			publishRecord(t, p, providerkit.ClassProduction, "terraform", postgresRecord("orders", "terraform"))
		})
		if !strings.Contains(message, "sst-pg-orders") {
			t.Errorf("refusal = %q, want it to name the external name it looked for", message)
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
