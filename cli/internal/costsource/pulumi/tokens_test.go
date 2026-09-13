package pulumi

import (
	"os"
	"strings"
	"testing"
)

func TestEveryPricedTypeIsReachableFromThePulumiTokenForIt(t *testing.T) {
	raw, err := os.ReadFile("testdata/priced_tokens.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("%q is not a vendor, a priced type and the pulumi token for it", line)
		}
		vendor, tf, typ := fields[0], fields[1], fields[2]
		gotVendor, gotTF, ok := token(typ)
		if !ok {
			t.Errorf("token(%q) was not read as a token at all", typ)
			continue
		}
		if gotVendor != vendor || gotTF != tf {
			t.Errorf("token(%q) = %q %q, want %q %q", typ, gotVendor, gotTF, vendor, tf)
		}
	}
}
