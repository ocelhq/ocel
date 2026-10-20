package account

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestANamespaceThatStartsANameWithAPrefixAWSReservesIsRefused(t *testing.T) {
	t.Parallel()

	refused := map[string]string{
		"internal":        "internal-",
		"internal-x":      "internal-",
		"sg":              "sg-",
		"sg-abcdefghijkl": "sg-",
		"sthree":          "sthree-",
		"amzn-s3-demo":    "amzn-s3-demo-",
		"aws":             "aws",
		"awsome":          "aws",
		"ssm":             "ssm",
		"ssm-team":        "ssm",
	}
	for namespace, prefix := range refused {
		err := Namespace(namespace).RefuseReservedPrefix()
		if code, isRefusal := provider.RefusedCode(err); !isRefusal || code != refusal.CodeInvalid || !strings.Contains(err.Error(), prefix) {
			t.Errorf("namespace %q = %v, want it refused as invalid naming the reserved prefix %q", namespace, err, prefix)
		}
	}
	for _, namespace := range []string{"ocel", "internals", "internal-platform", "sgx", "sthreex", "amzn-s3", "awe", "ss"} {
		if err := Namespace(namespace).RefuseReservedPrefix(); err != nil {
			t.Errorf("namespace %q = %v, want it accepted: no name derived from it starts with a reserved prefix", namespace, err)
		}
	}
}
