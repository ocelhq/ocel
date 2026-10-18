package gate_test

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge/gate"
)

func TestAMountedGateDecodesToTheGateItEncodedFrom(t *testing.T) {
	t.Parallel()

	held := gate.Gate{Key: "k", PasswordHash: "hash", BypassSecrets: []string{"one"}, AllowOptions: []string{"/api"}}
	encoded, err := gate.EncodeMounted(&held)
	if err != nil {
		t.Fatal(err)
	}

	got, err := gate.DecodeMounted(encoded)

	if err != nil || got == nil || !reflect.DeepEqual(*got, held) {
		t.Errorf("DecodeMounted = %+v, %v, want %+v", got, err, held)
	}
}

func TestAMountedGateThatWithdrawsTheGateDecodesToNoGate(t *testing.T) {
	t.Parallel()

	encoded, err := gate.EncodeMounted(nil)
	if err != nil {
		t.Fatal(err)
	}

	got, err := gate.DecodeMounted(encoded)

	if err != nil || got != nil {
		t.Errorf("DecodeMounted = %+v, %v, want no gate and no error", got, err)
	}
}

func TestAMountedGateThatCannotBeReadIsAnErrorRatherThanAnOpenHost(t *testing.T) {
	t.Parallel()

	for name, contents := range map[string]string{
		"empty":        "",
		"not json":     "{",
		"no key":       `{"key":"","passwordHash":"h"}`,
		"wrong shape":  `[]`,
		"a bare value": `true`,
	} {
		got, err := gate.DecodeMounted([]byte(contents))
		if err == nil || got != nil {
			t.Errorf("%s: DecodeMounted(%q) = %+v, %v, want an error", name, contents, got, err)
		}
	}
}
