package bucket

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestTwoBucketsWhoseNamesShareTheirFirstSixtyThreeCharactersStayApartAndFitTheLimit(t *testing.T) {
	t.Parallel()

	ref := provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("prod")}
	shared := strings.Repeat("uploads-", 8)
	first, second := mustName(t, ref, shared+"avatars"), mustName(t, ref, shared+"invoices")

	for _, name := range []string{first, second} {
		if len(name) > storeBucketNameMax {
			t.Errorf("bucket %s is %d characters, over the store's %d", name, len(name), storeBucketNameMax)
		}
		if !strings.HasPrefix(name, "prod-infra-uploads-") {
			t.Errorf("bucket %s does not begin with the stack and resource it holds", name)
		}
		tag := name[strings.LastIndex(name, naming.WordSeparator)+1:]
		if len(tag) != 6 {
			t.Errorf("bucket %s ends in %q, want the 6-character hash tag every fitted name ends in", name, tag)
		}
	}
	if first == second {
		t.Errorf("two resources cut to the same name %s and would share one bucket", first)
	}
}

func TestAShortBucketNameKeepsItsStackAndResourceWhole(t *testing.T) {
	t.Parallel()

	ref := provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("prod")}

	name := mustName(t, ref, "uploads")

	if head := name[:strings.LastIndex(name, naming.WordSeparator)]; head != "prod-infra-uploads" {
		t.Errorf("bucket %s reads %q before its tag, want prod-infra-uploads", name, head)
	}
}

func mustName(t *testing.T, ref provider.StackRef, resource string) string {
	t.Helper()
	name, err := storeBucketName(ref, resource)
	if err != nil {
		t.Fatalf("storeBucketName(%q) = %v", resource, err)
	}
	return name
}
