package proxy_test

import (
	"slices"
	"testing"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/runtime/proxy"
)

func TestEveryProxiedTypeThisProviderServesIsOneTheRuntimeRuns(t *testing.T) {
	t.Parallel()

	var proxied []bindingsv1.BindingType
	for _, kind := range deploy.Serves() {
		if providerkit.Proxied(kind) {
			proxied = append(proxied, providerkit.WireBindingType(kind))
		}
	}
	var run []bindingsv1.BindingType
	for wire := range bindingsv1.BindingType_name {
		kind := bindingsv1.BindingType(wire)
		if proxy.Serves(kind) {
			run = append(run, kind)
		}
	}
	slices.Sort(proxied)
	slices.Sort(run)
	if !slices.Equal(proxied, run) {
		t.Errorf("this provider says it serves %v of the types an app reaches through the runtime and the runtime runs %v: preflight lets a deploy past on the first list, and the app meets the second one at its first call",
			proxied, run)
	}
}
