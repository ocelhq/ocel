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

func bindingRequest(name string, kind linksv1.LinkType) *contractv1.DeployRequest {
	req := deployRequest()
	req.Manifest.Resources = []*contractv1.ManifestResource{{
		LogicalName: name,
		Linked:      true,
		Resource:    &resourcesv1.ResourceIdentifier{Type: kind, Name: name},
	}}
	return req
}

func publishRecord(t *testing.T, provider *fake.Provider, class providerkit.Class, owner string, link *linksv1.Link) {
	t.Helper()
	pair, err := providerkit.LinkPair(owner, link)
	if err != nil {
		t.Fatalf("LinkPair: %v", err)
	}
	store := values.Store{Records: provider.Records(), Sealer: provider.Sealer()}
	scope := values.Scope{Project: "shop", Class: class}
	if _, err := store.SetLinks(context.Background(), scope, "", owner, []values.Publishing{{Name: link.GetName(), Pair: pair}}); err != nil {
		t.Fatalf("SetLinks: %v", err)
	}
}

func postgresRecord(name, source string) *linksv1.Link {
	return &linksv1.Link{
		Name:   name,
		Source: source,
		Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{
			Host: "db.example", Port: 5432, Database: "orders", Username: "app", Password: "hunter2",
		}},
	}
}

func bucketRecord(name, source string) *linksv1.Link {
	return &linksv1.Link{
		Name:       name,
		Source:     source,
		Properties: &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{Bucket: "uploads-1"}},
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
	t.Run("names the link and the coordinate it looked in", func(t *testing.T) {
		message := refusedDeploy(t, bindingRequest("orders", linksv1.LinkType_LINK_TYPE_POSTGRES), nil)
		for _, want := range []string{"orders", "prod", "Nothing at all is published"} {
			if !strings.Contains(message, want) {
				t.Errorf("refusal = %q, want it to carry %q", message, want)
			}
		}
	})

	t.Run("points at the class the record was published to instead", func(t *testing.T) {
		message := refusedDeploy(t, bindingRequest("orders", linksv1.LinkType_LINK_TYPE_POSTGRES), func(p *fake.Provider) {
			publishRecord(t, p, providerkit.ClassPreview, "terraform", postgresRecord("orders", "terraform"))
		})
		if !strings.Contains(message, string(providerkit.ClassPreview)) {
			t.Errorf("refusal = %q, want it to name the class publishing the record", message)
		}
	})
}

func TestDeployRefusesABindingTheRecordCannotSatisfy(t *testing.T) {
	t.Run("a shape the app would cold-start against", func(t *testing.T) {
		message := refusedDeploy(t, bindingRequest("orders", linksv1.LinkType_LINK_TYPE_POSTGRES), func(p *fake.Provider) {
			publishRecord(t, p, providerkit.ClassProduction, "terraform", bucketRecord("orders", "terraform"))
		})
		if !strings.Contains(message, "cold start") {
			t.Errorf("refusal = %q, want the shape mismatch refused", message)
		}
	})

	t.Run("a bucket ocel's client did not provision", func(t *testing.T) {
		message := refusedDeploy(t, bindingRequest("uploads", linksv1.LinkType_LINK_TYPE_BUCKET), func(p *fake.Provider) {
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

		result, _ := deploy(t, client, bindingRequest("orders", linksv1.LinkType_LINK_TYPE_POSTGRES))
		if !result.GetSuccess() {
			t.Fatalf("Deploy() = %q, want a foreign postgres record bound", result.GetError())
		}
	})
}

func TestReadableAs(t *testing.T) {
	t.Run("refuses a custom record bound as a link", func(t *testing.T) {
		err := providerkit.ReadableAs(providerkit.Link{Name: "flags", Type: providerkit.LinkCustom}, providerkit.LinkPostgres)
		if err == nil || !strings.Contains(err.Error(), "transform") {
			t.Errorf("ReadableAs = %v, want a custom record sent to transforms", err)
		}
	})

	t.Run("admits a record of the declared type ocel provisioned", func(t *testing.T) {
		if err := providerkit.ReadableAs(providerkit.Link{Name: "uploads", Type: providerkit.LinkBucket}, providerkit.LinkBucket); err != nil {
			t.Errorf("ReadableAs = %v, want a record ocel published bound", err)
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
