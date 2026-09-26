package live

import (
	"errors"
	"strings"
	"testing"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

func postgresBinding() Binding {
	return Binding{
		Name: "db--main",
		Key:  "OCEL_RESOURCE_POSTGRES_main",
		Type: bindingsv1.BindingType_BINDING_TYPE_POSTGRES,
	}
}

const postgresRecord = `{"name":"db--main","postgres":{"host":"h","port":5432,"database":"d","username":"u","password":"p"}}`

func TestConform(t *testing.T) {
	t.Parallel()

	t.Run("names a value that is not a record without repeating a byte of it", func(t *testing.T) {
		t.Parallel()

		const secret = "xq7#!zv%"
		binding := postgresBinding()
		err := Conform([]Binding{binding}, map[string]string{binding.Key: secret})
		if err == nil {
			t.Fatal("Conform = nil, want a value that is not a record refused")
		}
		if !errors.Is(err, ErrDrift) {
			t.Errorf("error = %v, want it named as drift", err)
		}
		for _, b := range []byte(secret) {
			if strings.ContainsRune(err.Error(), rune(b)) {
				t.Errorf("error = %v, which contains %q from a value this deployment must treat as a credential", err, string(b))
			}
		}
		for _, want := range []string{binding.Name, binding.Key, binding.Type.String()} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("names a record that has no properties", func(t *testing.T) {
		t.Parallel()

		binding := postgresBinding()
		err := Conform([]Binding{binding}, map[string]string{binding.Key: `{"name":"db--main"}`})
		if err == nil {
			t.Fatal("Conform = nil, want a record with no properties refused")
		}
		if !errors.Is(err, ErrDrift) {
			t.Errorf("error = %v, want it named as drift", err)
		}
		if strings.Contains(err.Error(), "  ") {
			t.Errorf("error = %v, which renders the missing token as a hole rather than naming it", err)
		}
		for _, want := range []string{binding.Name, binding.Key, binding.Type.String()} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("names a record of another type", func(t *testing.T) {
		t.Parallel()

		binding := postgresBinding()
		err := Conform([]Binding{binding}, map[string]string{binding.Key: `{"name":"db--main","bucket":{"bucket":"b"}}`})
		if err == nil {
			t.Fatal("Conform = nil, want a bucket record under a postgres binding refused")
		}
		if !errors.Is(err, ErrDrift) {
			t.Errorf("error = %v, want it named as drift", err)
		}
		for _, want := range []string{binding.Name, binding.Key, binding.Type.String(), bindingsv1.BindingType_BINDING_TYPE_BUCKET.String()} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("names a binding that published nothing", func(t *testing.T) {
		t.Parallel()

		binding := postgresBinding()
		err := Conform([]Binding{binding}, map[string]string{"DB_PASSWORD": "hunter2"})
		if err == nil {
			t.Fatal("Conform = nil, want a binding with no record refused")
		}
		if !errors.Is(err, ErrDrift) {
			t.Errorf("error = %v, want it named as drift", err)
		}
	})

	t.Run("accepts a record of the declared type", func(t *testing.T) {
		t.Parallel()

		binding := postgresBinding()
		values := map[string]string{binding.Key: postgresRecord, "DB_PASSWORD": "hunter2"}
		if err := Conform([]Binding{binding}, values); err != nil {
			t.Fatalf("Conform: %v", err)
		}
		if values[binding.Key] != postgresRecord || values["DB_PASSWORD"] != "hunter2" {
			t.Errorf("values = %v, want everything left as published", values)
		}
	})
}
