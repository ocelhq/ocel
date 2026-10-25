package provider_test

import (
	"slices"
	"testing"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/live"
)

func TestAStoredSecretIsReadLiveFromItsCellAndABoundOneFromItsRecord(t *testing.T) {
	values := provider.AppValues{Secrets: []provider.SecretRef{
		{Key: "API_TOKEN", Folder: "/web"},
		{Key: "CLOUD_SEAL_KEY", Binding: "keys", Property: "seal"},
	}}

	if got, want := values.LiveKeys(), []live.Key{{Key: "API_TOKEN", Folder: "/web"}}; !slices.Equal(got, want) {
		t.Errorf("LiveKeys() = %v, want %v: a bound secret has no cell to read", got, want)
	}
	want := []live.Binding{{Name: "keys", Key: "CLOUD_SEAL_KEY", Type: bindingsv1.BindingType_BINDING_TYPE_CUSTOM, Property: "seal"}}
	if got := values.LiveBindings(); !slices.Equal(got, want) {
		t.Errorf("LiveBindings() = %v, want %v", got, want)
	}
}

func TestABoundResourceIsReadLiveUnderItsResourceName(t *testing.T) {
	values := provider.AppValues{Bindings: []provider.Binding{
		{Type: provider.BindingPostgres, Name: "pulumi-orders", Resource: "orders", Version: 3},
		{Type: provider.BindingBucket, Name: "uploads"},
	}}

	want := []live.Binding{
		{Name: "pulumi-orders", Key: "OCEL_RESOURCE_POSTGRES_orders", Type: bindingsv1.BindingType_BINDING_TYPE_POSTGRES, Granted: 3},
		{Name: "uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET},
	}
	if got := values.LiveBindings(); !slices.Equal(got, want) {
		t.Errorf("LiveBindings() = %v, want %v", got, want)
	}
}
