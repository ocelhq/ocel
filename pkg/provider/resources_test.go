package provider_test

import (
	"strings"
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
		CaPem: "-----BEGIN CERTIFICATE-----\nfixture\n-----END CERTIFICATE-----\n",
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
		t.Errorf("BindingMessage(BindingOf(record)) = %s on %s:%d as %q (tls %v, ca %q), want the kv record it was read from",
			message.GetName(), got.GetHost(), got.GetPort(), got.GetUsername(), got.GetTls(), got.GetCaPem())
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

func TestARealtimeBindingReadBackFromItsRecordIsTheSameRecord(t *testing.T) {
	t.Parallel()

	record := &bindingsv1.Binding{Name: "realtime--app", Properties: &bindingsv1.Binding_Realtime{Realtime: &bindingsv1.RealtimeProperties{
		Transport:  bindingsv1.RealtimeTransport_REALTIME_TRANSPORT_OCEL_GATEWAY,
		Url:        "wss://realtime.shop.example/ws",
		Host:       "realtime.shop.example",
		SigningKey: []byte{0x00, 0x01, 0xfe, 0xff},
		VerifyKey:  []byte{0x10, 0x20, 0x30},
	}}}
	binding := provider.BindingOf(record)
	if binding.Type != provider.BindingRealtime {
		t.Fatalf("BindingOf() type = %q, want %q", binding.Type, provider.BindingRealtime)
	}
	message, err := provider.BindingMessage(binding)
	if err != nil {
		t.Fatalf("BindingMessage: %v", err)
	}
	if !proto.Equal(message, record) {
		got := message.GetRealtime()
		t.Errorf("BindingMessage(BindingOf(record)) = %s over %s at %s (%s, verify key %x), want the realtime record it was read from",
			message.GetName(), got.GetTransport(), got.GetUrl(), got.GetHost(), got.GetVerifyKey())
	}
}

func realtimeProperties(without string) map[string]string {
	properties := map[string]string{
		provider.PropertyTransport:  "REALTIME_TRANSPORT_OCEL_GATEWAY",
		provider.PropertyURL:        "wss://realtime.shop.example/ws",
		provider.PropertyHost:       "realtime.shop.example",
		provider.PropertySigningKey: "AAH+/w==",
		provider.PropertyVerifyKey:  "ECAw",
	}
	delete(properties, without)
	return properties
}

func TestARealtimeBindingMissingWhatAnAppMintsOrConnectsWithIsRefused(t *testing.T) {
	t.Parallel()

	for _, missing := range []string{provider.PropertyTransport, provider.PropertyURL, provider.PropertyHost, provider.PropertySigningKey, provider.PropertyVerifyKey} {
		_, err := provider.BindingMessage(provider.Binding{Type: provider.BindingRealtime, Name: "realtime--app", Properties: realtimeProperties(missing)})
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("BindingMessage() without %s = %v, want it refused naming %s", missing, err, missing)
		}
	}
}

func TestARealtimeBindingOnATransportNoClientSpeaksIsRefused(t *testing.T) {
	t.Parallel()

	properties := realtimeProperties("")
	properties[provider.PropertyTransport] = "carrier-pigeon"
	_, err := provider.BindingMessage(provider.Binding{Type: provider.BindingRealtime, Name: "realtime--app", Properties: properties})
	if err == nil || !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Errorf("BindingMessage() = %v, want a transport no client speaks refused by name", err)
	}
}

func TestARealtimeBindingWhoseKeyIsNotBase64IsRefusedWithoutQuotingIt(t *testing.T) {
	t.Parallel()

	properties := realtimeProperties("")
	properties[provider.PropertySigningKey] = "not base64 s3cret!"
	_, err := provider.BindingMessage(provider.Binding{Type: provider.BindingRealtime, Name: "realtime--app", Properties: properties})
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("BindingMessage() = %v, want a signing key that is not base64 refused without quoting it", err)
	}
}
