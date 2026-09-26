package provider_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

func TestABucketBindingIncludesThePublicAddressItWasPublishedUnder(t *testing.T) {
	t.Parallel()

	message, err := provider.BindingMessage(provider.Binding{
		Type: provider.BindingBucket,
		Name: "uploads",
		Properties: map[string]string{
			provider.PropertyBucket:        "shop-prod-uploads",
			provider.PropertyPublicBaseURL: "https://storage.example.com/shop-prod-uploads",
		},
	})
	if err != nil {
		t.Fatalf("BindingMessage: %v", err)
	}

	bucket := message.GetBucket()
	if bucket.GetBucket() != "shop-prod-uploads" {
		t.Errorf("the binding names bucket %q, want the store the provider provisioned", bucket.GetBucket())
	}
	if bucket.GetPublicBaseUrl() != "https://storage.example.com/shop-prod-uploads" {
		t.Errorf("the binding has public base url %q, and without it an app declaring a public bucket has no address to hand out", bucket.GetPublicBaseUrl())
	}
}
