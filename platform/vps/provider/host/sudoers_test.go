package host

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func TestEachTierIsWhitelistedOnItsOwnSudoersLineNamingItsTierAlone(t *testing.T) {
	t.Parallel()

	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		fragment := written(Items(testAssets, tier, []byte(aKey+"\n"), ssh.AMD64, front.Proxy{}), machine.KindFile, sudoersSeal(tier))
		if fragment.Name == "" {
			t.Fatalf("bootstrapping %s writes no sudoers line of its own", tier)
		}
		line := string(fragment.Content)
		other := string(environment.TierProduction)
		if tier == environment.TierProduction {
			other = string(environment.TierPreview)
		}
		for _, banned := range []string{" " + other + " ", " init", LiveBinary + " " + boxapi.SealCommand + ","} {
			if strings.Contains(line, banned) {
				t.Errorf("%s's line reads %q and contains %q, which lets the deploy login past the tier or the verbs it needs", tier, line, banned)
			}
		}
		for _, wanted := range []string{LiveBinary + " " + boxapi.SealCommand + " " + string(tier) + " seal *", LiveBinary + " " + boxapi.SealCommand + " " + string(tier) + " open *"} {
			if !strings.Contains(line, wanted) {
				t.Errorf("%s's line reads %q and never grants %q", tier, line, wanted)
			}
		}
		granted := strings.TrimPrefix(strings.TrimSpace(line), deployUser+" ALL=(root) NOPASSWD: ")
		for _, allowed := range strings.Split(granted, ", ") {
			if strings.ContainsAny(allowed, ":=\\") {
				t.Errorf("%s's line grants %q, and sudoers reads an unescaped ':', '=' or '\\' inside a command's arguments as syntax", tier, allowed)
			}
		}
	}
}

func TestThePinsAreReadThroughOneSudoersLineThatNamesOnlyThePinHelper(t *testing.T) {
	t.Parallel()

	line := written(Items(testAssets, environment.TierProduction, []byte(aKey+"\n"), ssh.AMD64, front.Proxy{}), machine.KindFile, sudoersPins)
	if want := deployUser + " ALL=(root) NOPASSWD: " + LiveBinary + " " + boxapi.PinCommand + " *\n"; string(line.Content) != want || line.Mode != 0o440 || line.Owner != machine.RootOwner {
		t.Fatalf("%s holds %q at %04o owned by %q, want %q at 0440 owned by root: any other command there reads %s as root", sudoersPins, line.Content, line.Mode, line.Owner, want, boxapi.PinsDir)
	}
	granted := slices.ContainsFunc(grants(environment.TierProduction, ssh.AMD64, front.Proxy{}), func(grant Grant) bool {
		return strings.Contains(grant.Name, sudoersPins) && strings.Contains(grant.Detail, strings.TrimSpace(string(line.Content)))
	})
	if !granted {
		t.Errorf("the grants never name %s and the line it holds", sudoersPins)
	}
	if adopted := written(Items(testAssets, environment.TierProduction, nil, ssh.AMD64, coolifysCaddy()), machine.KindFile, sudoersPins); adopted.Name != "" {
		t.Errorf("behind Coolify's Caddy bootstrap writes %s, want none: ocel's proxy is what loads %s", sudoersPins, boxapi.PinsDir)
	}
}

func TestTheLineThatReadsThePinsGoesWithOcelsOwnProxy(t *testing.T) {
	t.Parallel()

	takes := func(removals []machine.Removal) bool {
		return slices.ContainsFunc(removals, func(r machine.Removal) bool { return r.Kind == machine.KindFile && r.Path == sudoersPins })
	}
	if !takes(removing(Reading{Assets: testAssets, Tier: environment.TierProduction, Observed: map[string]string{machine.KindFile + " " + sudoersPins: ""}}, Reading{Assets: testAssets, Tier: environment.TierPreview}, appsPresent{})) {
		t.Errorf("destroying the last tier leaves %s behind", sudoersPins)
	}
	if !takes((&frontMove{from: front.Proxy{}, to: coolifysCaddy()}).listRemovals()) {
		t.Errorf("moving off ocel's own proxy leaves %s behind", sudoersPins)
	}
}
