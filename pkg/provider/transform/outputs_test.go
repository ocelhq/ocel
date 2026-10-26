package transform_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type published []provider.Binding

func (p published) Names(context.Context) ([]string, error) {
	names := make([]string, 0, len(p))
	for _, record := range p {
		names = append(names, record.Name)
	}
	return names, nil
}

func (p published) Published(context.Context) ([]provider.Binding, error) { return p, nil }

func (p published) Named(_ context.Context, name string) (provider.Binding, error) {
	for _, record := range p {
		if record.Name == name {
			return record, nil
		}
	}
	return provider.Binding{}, refusal.Refuse(refusal.CodeInvalid, "nothing published %s", name)
}

func output(kind, name, property string) map[string]any {
	return map[string]any{"$ocelOutput": map[string]any{"type": kind, "name": name, "property": property}}
}

func infraSpec(bindings provider.Bindings) provider.StackSpec {
	return provider.StackSpec{
		Ref: provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("prod")},
		Resources: []provider.Resource{
			{Name: "uploads", Type: provider.BindingBucket},
			{Name: "assets", Type: provider.BindingBucket, Binding: "shared-assets"},
		},
		Bindings: bindings,
	}
}

func requestFor(names ...string) transform.Request {
	req := transform.Request{Provider: "gcp", EnvTier: "production", Env: "prod"}
	for _, name := range names {
		req.Resources = append(req.Resources, transform.Resource{Type: "bucket", Name: name})
	}
	return req
}

func TestABindingOutputIsFilledFromTheRecordItNames(t *testing.T) {
	bindings := published{
		{Name: "keys", Properties: map[string]string{"keyName": "projects/p/locations/l/keyRings/r/cryptoKeys/k"}},
		{Name: "shared-assets", Properties: map[string]string{"bucket": "acme-logs"}},
	}
	results := []transform.Result{{Patches: transform.Patches{"bucket": {
		"encryption": map[string]any{"defaultKmsKeyName": output("custom", "keys", "keyName")},
		"logging":    map[string]any{"logBucket": output("bucket", "assets", "bucket")},
	}}}}

	if err := transform.ResolveOutputs(context.Background(), infraSpec(bindings), requestFor("uploads"), results); err != nil {
		t.Fatalf("ResolveOutputs() = %v", err)
	}

	patch := results[0].Patches["bucket"]
	if got := patch["encryption"].(map[string]any)["defaultKmsKeyName"]; got != "projects/p/locations/l/keyRings/r/cryptoKeys/k" {
		t.Errorf("the key name was filled with %v, want the custom record's keyName", got)
	}
	if got := patch["logging"].(map[string]any)["logBucket"]; got != "acme-logs" {
		t.Errorf("the log bucket was filled with %v, want the bucket the config binds assets to", got)
	}
}

func TestABindingOutputOfAResourceTheDeployProvisionsIsRefused(t *testing.T) {
	results := []transform.Result{{Patches: transform.Patches{"bucket": {
		"logging": map[string]any{"logBucket": output("bucket", "uploads", "bucket")},
	}}}}

	err := transform.ResolveOutputs(context.Background(), infraSpec(published{}), requestFor("uploads"), results)

	var provisioned *transform.ProvisionedOutputError
	if !errors.As(err, &provisioned) {
		t.Fatalf("ResolveOutputs() = %v, want the read of a resource this deploy provisions refused", err)
	}
}

func TestABindingOutputThatResolvesToNothingNeverLandsInAPatch(t *testing.T) {
	bindings := published{{Name: "legacy", Properties: map[string]string{"logBucket": ""}}}
	results := []transform.Result{{Patches: transform.Patches{"bucket": {
		"logging": map[string]any{"logBucket": output("custom", "legacy", "logBucket")},
	}}}}

	err := transform.ResolveOutputs(context.Background(), infraSpec(bindings), requestFor("uploads"), results)

	var empty *transform.EmptyOutputError
	if !errors.As(err, &empty) {
		t.Fatalf("ResolveOutputs() = %v, want an EmptyOutputError rather than an empty value in the patch", err)
	}
}
