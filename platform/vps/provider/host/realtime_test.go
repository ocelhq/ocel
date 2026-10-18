package host

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func TestABootstrapPlacesTheRealtimeGatewayRootOwnedForItsContainersToMountReadOnly(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	at := slices.IndexFunc(Items(tier, []byte(aKey+"\n"), ssh.AMD64, front.Proxy{}), func(item machine.Item) bool { return item.Name == RealtimeBinary })
	if at < 0 {
		t.Fatalf("a bootstrap places no %s, and every realtime gateway runs it", RealtimeBinary)
	}
	placed := Items(tier, []byte(aKey+"\n"), ssh.AMD64, front.Proxy{})[at]
	if placed.Kind != machine.KindFile || placed.Owner != machine.RootOwner || placed.Mode != 0o755 || !bytes.Equal(placed.Content, realtimeBinary(ssh.AMD64)) {
		t.Errorf("%s is placed as %s %s %o, want the gateway built for the box, root's and executable by the container's user", RealtimeBinary, placed.Kind, placed.Owner, placed.Mode)
	}

	box := bootstrappedOn(t, tier)
	box.installed[tier] = slices.DeleteFunc(box.installed[tier], func(item machine.Item) bool { return item.Name == RealtimeBinary })
	if err := NewBootstrap(box.host(), testVendor, "shop").Apply(context.Background(),
		provider.BootstrapRequest{Tier: tier, WrittenBy: "the-suite"}, progress.Discard()); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if box.at(RealtimeBinary) < 0 {
		t.Errorf("an apply over a box without %s never placed it:\n%s", RealtimeBinary, strings.Join(box.commands(), "\n"))
	}
}
