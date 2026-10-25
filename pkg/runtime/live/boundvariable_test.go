package live

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

const publishedKeys = `{"binding":{"name":"keys","source":"pulumi","custom":{"seal":"k3y","rotation":2}},"secrets":["seal"]}`

func sealKey() Binding {
	return Binding{Name: "keys", Key: "CLOUD_SEAL_KEY", Type: bindingsv1.BindingType_BINDING_TYPE_CUSTOM, Property: "seal"}
}

func TestABoundSecretReachesTheAppAsTheOnePropertyItNames(t *testing.T) {
	t.Parallel()

	rotation := Binding{Name: "keys", Key: "CLOUD_SEAL_ROTATION", Type: bindingsv1.BindingType_BINDING_TYPE_CUSTOM, Property: "rotation"}
	reader := &fakeReader{records: []variablestore.StoredBinding{{Name: "keys", Value: []byte(publishedKeys)}}}
	values, _, err := Fetch(context.Background(), reader, nil, []Binding{sealKey(), rotation})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if values["CLOUD_SEAL_KEY"] != "k3y" || values["CLOUD_SEAL_ROTATION"] != "2" {
		t.Errorf("values = %v, want the seal property as it is and the rotation as its JSON", values)
	}
	if !slices.Equal(reader.names, []string{"keys"}) {
		t.Errorf("read names %v, want the record read once for both of its properties", reader.names)
	}
	if err := Conform([]Binding{sealKey(), rotation}, values); err != nil {
		t.Errorf("Conform = %v, want a delivered property read as the variable it is", err)
	}
	if keys := Keys(nil, []Binding{sealKey()}); !slices.Equal(keys, []string{"CLOUD_SEAL_KEY"}) {
		t.Errorf("Keys = %v, want the variable's own name among the live keys", keys)
	}
}

func TestABoundSecretWhoseRecordNoLongerPublishesItsPropertyIsDrift(t *testing.T) {
	t.Parallel()

	for _, stored := range []string{
		`{"binding":{"name":"keys","source":"pulumi","custom":{"other":"x"}}}`,
		`{"binding":{"name":"keys","source":"pulumi","postgres":{"host":"h"}}}`,
	} {
		reader := &fakeReader{records: []variablestore.StoredBinding{{Name: "keys", Value: []byte(stored)}}}
		values, _, err := Fetch(context.Background(), reader, nil, []Binding{sealKey()})
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		err = Conform([]Binding{sealKey()}, values)
		if !errors.Is(err, ErrDrift) || !strings.Contains(err.Error(), "seal") {
			t.Errorf("Conform(%s) = %v, want drift naming the property the variable reads", stored, err)
		}
	}
}

func TestABoundSecretKeepsTheRecordAndPropertyItReadsInTheRuntimeManifest(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(sealKey())
	if err != nil {
		t.Fatal(err)
	}
	var decoded Binding
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != sealKey() {
		t.Errorf("decoded %+v from %s, want %+v", decoded, encoded, sealKey())
	}
}
