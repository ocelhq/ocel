package host

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const testVendor provider.Vendor = "vps"

func currentHost() Reading {
	tier := environment.TierProduction
	keys := []byte(aKey + "\n")
	return Reading{Assets: testAssets,
		Arch:     ssh.AMD64,
		Tier:     tier,
		Present:  true,
		Keys:     keys,
		Stamp:    machine.Stamp{State: machine.StateComplete, Digests: machine.DigestsOf(Items(testAssets, tier, keys, ssh.AMD64, front.Proxy{}))},
		Observed: machine.DigestsOf(Items(testAssets, tier, keys, ssh.AMD64, front.Proxy{})),
	}
}

func drifted(t *testing.T, read Reading, name string) Reading {
	t.Helper()

	for _, item := range Items(testAssets, read.Tier, read.Keys, ssh.AMD64, front.Proxy{}) {
		if item.Name != name {
			continue
		}
		read.Observed[item.ID()] = "a digest nothing this ocel writes produces"
		return read
	}
	t.Fatalf("nothing in the item set is named %s", name)
	return read
}

func refusalOf(t *testing.T, err error, code refusal.Code) refusal.Refusal {
	t.Helper()

	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("error = %v, want a refusal the CLI can render", err)
	}
	if refused.Code != code {
		t.Fatalf("refusal code = %q, want %q", refused.Code, code)
	}
	return refused
}

func TestAReplacementRefusingApplyInstallsWhatIsAbsent(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	fresh := Reading{Assets: testAssets, Arch: ssh.AMD64, Tier: tier, Observed: map[string]string{}}
	if err := refuseReplacements(fresh, Items(testAssets, tier, nil, ssh.AMD64, front.Proxy{})); err != nil {
		t.Errorf("a replacement-refusing apply over a machine nothing has bootstrapped = %v, want the installs to proceed", err)
	}
}

func TestAReplacementRefusingApplyWillNotWriteOverWhatAlreadyExists(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	for _, name := range []string{LiveBinary, SealKeyPath(tier), deployUser, dockerEngine} {
		read := drifted(t, currentHost(), name)
		refused := refusalOf(t, refuseReplacements(read, Items(testAssets, read.Tier, read.Keys, ssh.AMD64, front.Proxy{})), refusal.CodeNotReady)
		if !strings.Contains(refused.Message, name) {
			t.Errorf("the refusal says %q, want it to name %s as the thing it would write over", refused.Message, name)
		}
		if !strings.Contains(refused.Message, "--yes") {
			t.Errorf("the refusal says %q, want it to name the flag that accepts the write", refused.Message)
		}
	}
}

func TestAReplacementRefusingApplyConvergesAHostRatherThanRefusingEveryChange(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	for _, name := range []string{dockerUnit, KeyValuesDir(tier), boxapi.StateRoot, boxapi.LibDir} {
		read := drifted(t, currentHost(), name)
		if err := refuseReplacements(read, Items(testAssets, read.Tier, read.Keys, ssh.AMD64, front.Proxy{})); err != nil {
			t.Errorf("a replacement-refusing apply over a host whose %s has moved = %v, want a converge that destroys nothing to proceed", name, err)
		}
	}
}
