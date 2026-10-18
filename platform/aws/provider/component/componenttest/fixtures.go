package componenttest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/bindings"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

func FixtureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(RepoRoot(t), "proto", "common", "bindings", "v1", "fixtures")
}

func FixtureFile(typ bindingsv1.BindingType) string {
	return strings.ToLower(bindings.EnvFragment(typ)) + ".json"
}

func FixtureBytes(t *testing.T, typ bindingsv1.BindingType) []byte {
	t.Helper()
	path := filepath.Join(FixtureDir(t), FixtureFile(typ))
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read binding fixture for %s: %v", typ, err)
	}
	return body
}

func BindingFixture(t *testing.T, typ bindingsv1.BindingType) *bindingsv1.Binding {
	t.Helper()
	binding := &bindingsv1.Binding{}
	if err := protojson.Unmarshal(FixtureBytes(t, typ), binding); err != nil {
		t.Fatalf("binding fixture %s is not a common.bindings.v1.Binding: %v", FixtureFile(typ), err)
	}
	return binding
}

func PublishedFixtureBytes(t *testing.T, typ bindingsv1.BindingType) []byte {
	t.Helper()
	path := filepath.Join(RepoRoot(t), "proto", "provider", "variablestore", "v1", "fixtures", FixtureFile(typ))
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read published fixture for %s: %v", typ, err)
	}
	return body
}

func PublishedFixture(t *testing.T, typ bindingsv1.BindingType) *variablestorev1.PublishedBinding {
	t.Helper()
	published := &variablestorev1.PublishedBinding{}
	if err := protojson.Unmarshal(PublishedFixtureBytes(t, typ), published); err != nil {
		t.Fatalf("published fixture %s is not a provider.variablestore.v1.PublishedBinding: %v", FixtureFile(typ), err)
	}
	return published
}

func CanonicalJSON(t *testing.T, raw []byte) []byte {
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

func AssertMatchesFixture(t *testing.T, got *variablestorev1.PublishedBinding, typ bindingsv1.BindingType) {
	t.Helper()
	want := PublishedFixture(t, typ)
	if !proto.Equal(got, want) {
		t.Errorf("producer published a %s binding that differs from the checked-in record %s at %s — the consumer suite parses that fixture, so a divergence here is cross-language drift", typ, FixtureFile(typ), strings.Join(differingFields(got.ProtoReflect(), want.ProtoReflect(), ""), "; "))
	}

	stored, err := variablestoreserver.EncodePublishedBinding(got)
	if err != nil {
		t.Fatalf("encode the produced binding as the store does: %v", err)
	}
	if wantBytes := CanonicalJSON(t, PublishedFixtureBytes(t, typ)); !bytes.Equal(CanonicalJSON(t, stored), wantBytes) {
		t.Errorf("published record = %s, want the checked-in published fixture byte for byte %s", stored, wantBytes)
	}
	payload, err := protojson.Marshal(got.GetBinding())
	if err != nil {
		t.Fatalf("encode the produced binding as the app payload: %v", err)
	}
	if wantBytes := CanonicalJSON(t, FixtureBytes(t, typ)); !bytes.Equal(CanonicalJSON(t, payload), wantBytes) {
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

func RepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.work found above the working directory")
		}
		dir = parent
	}
}
