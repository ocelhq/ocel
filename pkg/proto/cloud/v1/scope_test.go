package cloudv1_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
)

func TestEveryScopeNamesTheClaimATokenCarriesForIt(t *testing.T) {
	values := cloudv1.Scope(0).Descriptor().Values()
	claims := map[string]bool{}
	for i := range values.Len() {
		value := values.Get(i)
		claim := proto.GetExtension(value.Options(), cloudv1.E_Claim).(string)
		if value.Number() == 0 {
			if claim != "" {
				t.Errorf("%s names the claim %q, want none", value.Name(), claim)
			}
			continue
		}
		if !strings.HasPrefix(claim, "cloud.") || claims[claim] {
			t.Errorf("%s names the claim %q, want a cloud. claim no other scope names", value.Name(), claim)
		}
		claims[claim] = true
	}
}
