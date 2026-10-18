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

func TestTopicAndTaskBindingsReadBackFromTheirRecordsAreTheSameRecords(t *testing.T) {
	t.Parallel()

	for _, record := range []*bindingsv1.Binding{
		{Name: "orders", Properties: &bindingsv1.Binding_Topic{Topic: &bindingsv1.TopicProperties{}}},
		{Name: "send-email", Properties: &bindingsv1.Binding_Task{Task: &bindingsv1.TaskProperties{}}},
	} {
		message, err := provider.BindingMessage(provider.BindingOf(record))
		if err != nil {
			t.Fatalf("BindingMessage(%s): %v", record.GetName(), err)
		}
		if !proto.Equal(message, record) {
			t.Errorf("BindingMessage(BindingOf(%s)) is not the record it was read from: an app binds a task or topic by its type", record.GetName())
		}
	}
}

func TestAKVBindingReadBackFromItsRecordIsTheSameRecord(t *testing.T) {
	t.Parallel()

	record := &bindingsv1.Binding{Name: "kv--cache", Properties: &bindingsv1.Binding_Kv{Kv: &bindingsv1.KvProperties{
		Host: "127.0.0.1", Port: 6380, Username: "app", Password: "fixture-password", Tls: true,
		CaPem: "-----BEGIN CERTIFICATE-----\nfixture\n-----END CERTIFICATE-----\n", TlsServerName: "cache.internal",
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

func TestAPostgresBindingReadBackFromItsRecordKeepsHowItIsReached(t *testing.T) {
	t.Parallel()

	for _, record := range []*bindingsv1.Binding{
		{Name: "orders", Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
			Host: "10.240.0.9", Port: 5432, Database: "ocel", Username: "ocel", Password: "fixture-password",
			TlsMode: bindingsv1.PostgresTlsMode_POSTGRES_TLS_MODE_REQUIRE,
		}}},
		{Name: "forwarded", Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
			Host: "127.0.0.1", Port: 41234, Database: "ocel", Username: "ocel", Password: "fixture-password",
			TlsMode: bindingsv1.PostgresTlsMode_POSTGRES_TLS_MODE_VERIFY_FULL, TlsServerName: "orders.cluster.internal",
		}}},
		{Name: "ledger", Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
			Host: "db.example.com", Port: 5432, Database: "ledger", Username: "app", Password: "fixture-password",
			Url:     "postgres://app:fixture-password@db.example.com:5432/ledger",
			TlsMode: bindingsv1.PostgresTlsMode_POSTGRES_TLS_MODE_VERIFY_FULL, TlsCa: "-----BEGIN CERTIFICATE-----\nfixture\n-----END CERTIFICATE-----\n",
		}}},
	} {
		message, err := provider.BindingMessage(provider.BindingOf(record))
		if err != nil {
			t.Fatalf("BindingMessage(%s): %v", record.GetName(), err)
		}
		if !proto.Equal(message, record) {
			got := message.GetPostgres()
			t.Errorf("BindingMessage(BindingOf(%s)) reaches %s:%d under tls %v with ca %q and url %q, want the record it was read from: "+
				"a database that refuses plaintext is unreachable without its tls mode", record.GetName(), got.GetHost(), got.GetPort(), got.GetTlsMode(), got.GetTlsCa(), got.GetUrl())
		}
	}
}

func TestAPostgresBindingOnATLSModeNoClientSpeaksIsRefused(t *testing.T) {
	t.Parallel()

	_, err := provider.BindingMessage(provider.Binding{
		Type: provider.BindingPostgres,
		Name: "orders",
		Properties: map[string]string{
			provider.PropertyHost: "10.240.0.9", provider.PropertyPort: "5432", provider.PropertyDatabase: "ocel",
			provider.PropertyUsername: "ocel", provider.PropertyPassword: "fixture-password", provider.PropertyTLSMode: "SOMETIMES",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "SOMETIMES") {
		t.Errorf("BindingMessage() = %v, want a tls mode no client speaks refused by name", err)
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
		t.Errorf("BindingMessage(BindingOf(record)) = %s at %s (%s, verify key %x), want the realtime record it was read from",
			message.GetName(), got.GetUrl(), got.GetHost(), got.GetVerifyKey())
	}
}

func realtimeProperties(without string) map[string]string {
	properties := map[string]string{
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

	for _, missing := range []string{provider.PropertyURL, provider.PropertyHost, provider.PropertySigningKey, provider.PropertyVerifyKey} {
		_, err := provider.BindingMessage(provider.Binding{Type: provider.BindingRealtime, Name: "realtime--app", Properties: realtimeProperties(missing)})
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("BindingMessage() without %s = %v, want it refused naming %s", missing, err, missing)
		}
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

const testPasswordSecret = "arn:aws:secretsmanager:us-east-1:111122223333:secret:rds!cluster-0a1b2c3d-AbCdEf"

func postgresWithoutPassword(grants ...provider.Grant) provider.Binding {
	return provider.Binding{
		Type: provider.BindingPostgres,
		Name: "orders",
		Properties: map[string]string{
			provider.PropertyHost: "orders.cluster.internal", provider.PropertyPort: "5432",
			provider.PropertyDatabase: "ocel", provider.PropertyUsername: "ocel",
		},
		Grants: grants,
	}
}

func passwordSecretGrant(resources ...string) provider.Grant {
	return provider.Grant{Label: provider.GrantPasswordSecret, Actions: []string{"secretsmanager:GetSecretValue"}, Resources: resources}
}

func TestAPostgresBindingThatNamesThePasswordSecretInsteadOfCarryingThePasswordIsAccepted(t *testing.T) {
	t.Parallel()

	binding := postgresWithoutPassword(passwordSecretGrant(testPasswordSecret))

	if err := provider.VerifyProperties(binding); err != nil {
		t.Errorf("VerifyProperties() = %v, want a binding naming the secret its password rotates in accepted", err)
	}
	if got := binding.PasswordSecret(); got != testPasswordSecret {
		t.Errorf("PasswordSecret() = %q, want %s", got, testPasswordSecret)
	}
}

func TestAPostgresBindingWithNeitherAPasswordNorAPasswordSecretIsRefusedNamingBoth(t *testing.T) {
	t.Parallel()

	err := provider.VerifyProperties(postgresWithoutPassword())

	if err == nil || !strings.Contains(err.Error(), provider.PropertyPassword) || !strings.Contains(err.Error(), provider.GrantPasswordSecret) {
		t.Errorf("VerifyProperties() = %v, want it refused naming the password and the %q grant", err, provider.GrantPasswordSecret)
	}
}

func TestAPostgresBindingNamingMoreThanOnePasswordSecretIsRefused(t *testing.T) {
	t.Parallel()

	for name, binding := range map[string]provider.Binding{
		"two grants":             postgresWithoutPassword(passwordSecretGrant(testPasswordSecret), passwordSecretGrant(testPasswordSecret+"-other")),
		"one grant, two secrets": postgresWithoutPassword(passwordSecretGrant(testPasswordSecret, testPasswordSecret+"-other")),
	} {
		if err := provider.VerifyProperties(binding); err == nil {
			t.Errorf("VerifyProperties() of %s = nil, want it refused: a password is read from one secret", name)
		}
		if got := binding.PasswordSecret(); got != "" {
			t.Errorf("PasswordSecret() of %s = %q, want none", name, got)
		}
	}
}

func TestAPostgresBindingCarryingItsPasswordNamesNoPasswordSecret(t *testing.T) {
	t.Parallel()

	binding := postgresWithoutPassword()
	binding.Properties[provider.PropertyPassword] = "fixture-password"

	if err := provider.VerifyProperties(binding); err != nil {
		t.Errorf("VerifyProperties() = %v, want a binding carrying its password accepted", err)
	}
	if got := binding.PasswordSecret(); got != "" {
		t.Errorf("PasswordSecret() = %q, want none", got)
	}
}
