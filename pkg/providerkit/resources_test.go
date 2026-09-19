package providerkit_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestABucketBindingCarriesThePublicAddressItWasPublishedUnder(t *testing.T) {
	t.Parallel()

	message, err := providerkit.BindingMessage(providerkit.Binding{
		Type: providerkit.BindingBucket,
		Name: "uploads",
		Properties: map[string]string{
			providerkit.PropertyBucket:        "shop-prod-uploads",
			providerkit.PropertyPublicBaseURL: "https://storage.example.com/shop-prod-uploads",
		},
	})
	if err != nil {
		t.Fatalf("BindingMessage: %v", err)
	}

	held := message.GetBucket()
	if held.GetBucket() != "shop-prod-uploads" {
		t.Errorf("the binding names bucket %q, want the store the provider stood up", held.GetBucket())
	}
	if held.GetPublicBaseUrl() != "https://storage.example.com/shop-prod-uploads" {
		t.Errorf("the binding carries public base url %q, and without it an app declaring a public bucket has no address to hand out", held.GetPublicBaseUrl())
	}
}
