package deploy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/ocelhq/ocel/pkg/bindings"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

func fixtureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "proto", "common", "bindings", "v1", "fixtures")
}

func fixtureFile(typ bindingsv1.BindingType) string {
	return strings.ToLower(bindings.EnvFragment(typ)) + ".json"
}

func fixtureBytes(t *testing.T, typ bindingsv1.BindingType) []byte {
	t.Helper()
	path := filepath.Join(fixtureDir(t), fixtureFile(typ))
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read binding fixture for %s: %v", typ, err)
	}
	return body
}

func bindingFixture(t *testing.T, typ bindingsv1.BindingType) *bindingsv1.Binding {
	t.Helper()
	binding := &bindingsv1.Binding{}
	if err := protojson.Unmarshal(fixtureBytes(t, typ), binding); err != nil {
		t.Fatalf("binding fixture %s is not a common.bindings.v1.Binding: %v", fixtureFile(typ), err)
	}
	return binding
}

func publishedFixtureBytes(t *testing.T, typ bindingsv1.BindingType) []byte {
	t.Helper()
	path := filepath.Join(repoRoot(t), "proto", "provider", "variablestore", "v1", "fixtures", fixtureFile(typ))
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read published fixture for %s: %v", typ, err)
	}
	return body
}

func publishedFixture(t *testing.T, typ bindingsv1.BindingType) *variablestorev1.PublishedBinding {
	t.Helper()
	published := &variablestorev1.PublishedBinding{}
	if err := protojson.Unmarshal(publishedFixtureBytes(t, typ), published); err != nil {
		t.Fatalf("published fixture %s is not a provider.variablestore.v1.PublishedBinding: %v", fixtureFile(typ), err)
	}
	return published
}

func canonicalJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
	out, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("re-encode JSON: %v", err)
	}
	return out
}

func TestBindingFixtureExistsPerBindingType(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(fixtureDir(t))
	if err != nil {
		t.Fatalf("read fixture directory: %v", err)
	}
	var found []string
	for _, e := range entries {
		found = append(found, e.Name())
	}

	var want []string
	for _, typ := range bindings.Types() {
		want = append(want, fixtureFile(typ))
		if got := bindings.TypeOf(bindingFixture(t, typ)); got != typ {
			t.Errorf("fixture %s declares a %s, want %s", fixtureFile(typ), got, typ)
		}
	}

	slices.Sort(found)
	slices.Sort(want)
	if !slices.Equal(found, want) {
		t.Errorf("fixtures = %v, want exactly one per binding type %v", found, want)
	}
}

func assertMatchesFixture(t *testing.T, got *variablestorev1.PublishedBinding, typ bindingsv1.BindingType) {
	t.Helper()
	want := publishedFixture(t, typ)
	if !proto.Equal(got, want) {
		t.Errorf("producer published a %s binding that differs from the checked-in record %s at %s — the consumer suite parses that fixture, so a divergence here is cross-language drift", typ, fixtureFile(typ), strings.Join(differingFields(got.ProtoReflect(), want.ProtoReflect(), ""), "; "))
	}

	stored, err := variablestoreserver.EncodePublishedBinding(got)
	if err != nil {
		t.Fatalf("encode the produced binding as the store does: %v", err)
	}
	if wantBytes := canonicalJSON(t, publishedFixtureBytes(t, typ)); !bytes.Equal(canonicalJSON(t, stored), wantBytes) {
		t.Errorf("published record = %s, want the checked-in published fixture byte for byte %s", stored, wantBytes)
	}
	payload, err := protojson.Marshal(got.GetBinding())
	if err != nil {
		t.Fatalf("encode the produced binding as the app payload: %v", err)
	}
	if wantBytes := canonicalJSON(t, fixtureBytes(t, typ)); !bytes.Equal(canonicalJSON(t, payload), wantBytes) {
		t.Errorf("app payload = %s, want the checked-in fixture byte for byte %s", payload, wantBytes)
	}
}

func differingFields(got, want protoreflect.Message, prefix string) []string {
	var paths []string
	fields := got.Descriptor().Fields()
	for i := range fields.Len() {
		field := fields.Get(i)
		path := prefix + string(field.Name())
		gotValue, wantValue := got.Get(field), want.Get(field)
		switch {
		case gotValue.Equal(wantValue):
		case field.Message() != nil && !field.IsList() && !field.IsMap() && got.Has(field) && want.Has(field):
			paths = append(paths, differingFields(gotValue.Message(), wantValue.Message(), path+".")...)
		case field.Options().(*descriptorpb.FieldOptions).GetDebugRedact():
			paths = append(paths, path+" (redacted)")
		case field.Message() != nil || field.IsList() || field.IsMap():
			paths = append(paths, path)
		default:
			paths = append(paths, fmt.Sprintf("%s = %v, want %v", path, gotValue.Interface(), wantValue.Interface()))
		}
	}
	return paths
}

func TestPostgresProducerEmitsTheFixture(t *testing.T) {
	t.Parallel()

	want := bindingFixture(t, bindingsv1.BindingType_BINDING_TYPE_POSTGRES).GetPostgres()
	secretARN := provider.PublishedBindingOf(publishedFixture(t, bindingsv1.BindingType_BINDING_TYPE_POSTGRES)).PasswordSecret()
	if secretARN == "" {
		t.Fatalf("fixture %s names no %q grant, and the AWS record names its password by one", fixtureFile(bindingsv1.BindingType_BINDING_TYPE_POSTGRES), provider.GrantPasswordSecret)
	}

	fields := map[string]any{
		outputKeyHost:      want.GetHost(),
		outputKeyPort:      float64(want.GetPort()),
		outputKeyDatabase:  want.GetDatabase(),
		outputKeyUsername:  want.GetUsername(),
		outputKeySecretARN: secretARN,
	}

	got, err := collectPostgresBinding("us-east-1", "db--main", fields)
	if err != nil {
		t.Fatalf("collectPostgresBinding: %v", err)
	}
	if password := got.GetBinding().GetPostgres().GetPassword(); password != "" {
		t.Errorf("AWS emitted a password of %d bytes, want none: the runtime reads it from the secret the record names", len(password))
	}
	if named := provider.PublishedBindingOf(got).PasswordSecret(); named != secretARN {
		t.Errorf("AWS named %q as the password secret, want %s", named, secretARN)
	}
	got.GetBinding().GetPostgres().Password = want.GetPassword()
	assertMatchesFixture(t, got, bindingsv1.BindingType_BINDING_TYPE_POSTGRES)
}

const fixtureStateTableARN = "arn:aws:dynamodb:us-east-1:111122223333:table/ocel-state"

var fixtureSessions = newSessionScope("shop", "prod", fixtureStateTableARN)

func TestBucketProducerEmitsTheFixture(t *testing.T) {
	t.Parallel()

	want := bindingFixture(t, bindingsv1.BindingType_BINDING_TYPE_BUCKET).GetBucket()
	fields := map[string]any{outputKeyBucket: want.GetBucket()}

	got, err := collectBucketBinding("bucket--uploads", fixtureSessions, fields)
	if err != nil {
		t.Fatalf("collectBucketBinding: %v", err)
	}
	assertMatchesFixture(t, got, bindingsv1.BindingType_BINDING_TYPE_BUCKET)
}
