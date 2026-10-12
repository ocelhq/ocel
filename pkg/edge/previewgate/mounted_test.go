package previewgate_test

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
)

func TestAMountedGateDecodesToTheGateItEncodedFrom(t *testing.T) {
	t.Parallel()

	gate := previewgate.Gate{Key: "k", PasswordMAC: "mac", BypassSecrets: []string{"one"}, AllowOptions: []string{"/api"}}
	encoded, err := previewgate.EncodeMounted(&gate)
	if err != nil {
		t.Fatal(err)
	}

	got, err := previewgate.DecodeMounted(encoded)

	if err != nil || got == nil || !reflect.DeepEqual(*got, gate) {
		t.Errorf("DecodeMounted = %+v, %v, want %+v", got, err, gate)
	}
}

func TestAMountedGateThatWithdrawsTheGateDecodesToNoGate(t *testing.T) {
	t.Parallel()

	encoded, err := previewgate.EncodeMounted(nil)
	if err != nil {
		t.Fatal(err)
	}

	got, err := previewgate.DecodeMounted(encoded)

	if err != nil || got != nil {
		t.Errorf("DecodeMounted = %+v, %v, want no gate and no error", got, err)
	}
}

func TestAMountedGateThatCannotBeReadIsAnErrorRatherThanAnOpenPreview(t *testing.T) {
	t.Parallel()

	for name, contents := range map[string]string{
		"empty":        "",
		"not json":     "{",
		"no key":       `{"key":"","passwordMac":"m"}`,
		"wrong shape":  `[]`,
		"a bare value": `true`,
	} {
		got, err := previewgate.DecodeMounted([]byte(contents))
		if err == nil || got != nil {
			t.Errorf("%s: DecodeMounted(%q) = %+v, %v, want an error", name, contents, got, err)
		}
	}
}
