package host

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const testVendor provider.Vendor = "vps"

func currentHost() Reading {
	tier := environment.TierProduction
	keys := []byte(aKey + "\n")
	return Reading{
		Arch:     ssh.AMD64,
		Tier:     tier,
		Present:  true,
		Keys:     keys,
		Stamp:    Stamp{State: StateComplete, Digests: digests(Items(tier, keys, ssh.AMD64, Front{}))},
		Observed: digests(Items(tier, keys, ssh.AMD64, Front{})),
	}
}

func drifted(t *testing.T, read Reading, name string) Reading {
	t.Helper()

	for _, item := range Items(read.Tier, read.Keys, ssh.AMD64, Front{}) {
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
	fresh := Reading{Arch: ssh.AMD64, Tier: tier, Observed: map[string]string{}}
	if err := refuseReplacements(fresh, Items(tier, nil, ssh.AMD64, Front{})); err != nil {
		t.Errorf("a replacement-refusing apply over a machine nothing has bootstrapped = %v, want the installs to proceed", err)
	}
}

func TestAReplacementRefusingApplyWillNotWriteOverWhatAlreadyExists(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	for _, name := range []string{boxstore.KeyValuesHelper, SealKeyPath(tier), deployUser, dockerEngine} {
		read := drifted(t, currentHost(), name)
		refused := refusalOf(t, refuseReplacements(read, Items(read.Tier, read.Keys, ssh.AMD64, Front{})), refusal.CodeNotReady)
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
	for _, name := range []string{dockerUnit, KeyValuesDir(tier), stateRoot, boxstore.Dir} {
		read := drifted(t, currentHost(), name)
		if err := refuseReplacements(read, Items(read.Tier, read.Keys, ssh.AMD64, Front{})); err != nil {
			t.Errorf("a replacement-refusing apply over a host whose %s has moved = %v, want a converge that destroys nothing to proceed", name, err)
		}
	}
}
