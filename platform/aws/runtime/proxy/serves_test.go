package proxy_test

import (
	"slices"
	"testing"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/runtime/proxy"
)

func TestEveryProxiedTypeThisProviderServesIsOneTheRuntimeRuns(t *testing.T) {
	t.Parallel()

	var proxied []linksv1.LinkType
	for _, kind := range deploy.Serves() {
		if providerkit.Proxied(kind) {
			proxied = append(proxied, providerkit.WireLinkType(kind))
		}
	}
	var run []linksv1.LinkType
	for wire := range linksv1.LinkType_name {
		kind := linksv1.LinkType(wire)
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
