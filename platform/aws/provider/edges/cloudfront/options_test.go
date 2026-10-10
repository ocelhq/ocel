package cloudfront

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/configdoc/schematest"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestTheOriginShieldOptionIsReadFromTheEdgesOptions(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		options provider.Options
		want    bool
	}{
		"left out":   {provider.Options{}, true},
		"turned on":  {provider.Options{"originShield": true}, true},
		"turned off": {provider.Options{"originShield": false}, false},
	} {
		decoded, err := provider.DecodeEdgeOptions[Options](Kind, tc.options)
		if err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		if got := decoded.ShieldsOrigin(); got != tc.want {
			t.Errorf("%s: ShieldsOrigin() = %v, want %v", name, got, tc.want)
		}
	}
}

func TestEdgeSchemaIsCommitted(t *testing.T) {
	generated, err := configdoc.EdgeSchema(Kind, "CloudFront", Options{})
	if err != nil {
		t.Fatalf("edge schema: %v", err)
	}
	schematest.AssertCommitted(t, schematest.EdgeSchemaFile, generated)
}
