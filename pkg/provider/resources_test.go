package provider_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
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

func TestAKVBindingReadBackFromItsRecordIsTheSameRecord(t *testing.T) {
	t.Parallel()

	record := &bindingsv1.Binding{Name: "kv--cache", Properties: &bindingsv1.Binding_Kv{Kv: &bindingsv1.KvProperties{
		Host: "cache.internal", Port: 6380, Username: "app", Password: "fixture-password", Tls: true,
	}}}
	binding := provider.BindingOf(record)
	if binding.Type != provider.BindingKV {
		t.Fatalf("BindingOf() type = %q, want %q", binding.Type, provider.BindingKV)
	}
	message, err := provider.BindingMessage(binding)
	if err != nil {
		t.Fatalf("BindingMessage: %v", err)
	}
	if !proto.Equal(message, record) {
		got := message.GetKv()
		t.Errorf("BindingMessage(BindingOf(record)) = %s on %s:%d as %q (tls %v), want the kv record it was read from",
			message.GetName(), got.GetHost(), got.GetPort(), got.GetUsername(), got.GetTls())
	}
}

func TestAKVBindingWithNoPasswordIsRefused(t *testing.T) {
	t.Parallel()

	_, err := provider.BindingMessage(provider.Binding{
		Type:       provider.BindingKV,
		Name:       "kv--cache",
		Properties: map[string]string{provider.PropertyHost: "cache.internal", provider.PropertyPort: "6379"},
	})
	if err == nil {
		t.Error("BindingMessage() = nil, want a kv binding with no password refused: every store is reached with one")
	}
}
