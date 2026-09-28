package host

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
)

func TestEachTierIsWhitelistedOnItsOwnSudoersLineNamingItsTierAlone(t *testing.T) {
	t.Parallel()

	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		fragment := written(Items(tier, []byte(aKey+"\n"), ArchAMD64, Front{}), KindFile, sudoersSeal(tier))
		if fragment.Name == "" {
			t.Fatalf("bootstrapping %s writes no sudoers line of its own", tier)
		}
		line := string(fragment.Content)
		other := string(environment.TierProduction)
		if tier == environment.TierProduction {
			other = string(environment.TierPreview)
		}
		for _, banned := range []string{" " + other + " ", " init", boxstore.SealHelper + ","} {
			if strings.Contains(line, banned) {
				t.Errorf("%s's line reads %q and contains %q, which lets the deploy login past the tier or the verbs it needs", tier, line, banned)
			}
		}
		for _, wanted := range []string{boxstore.SealHelper + " " + string(tier) + " seal *", boxstore.SealHelper + " " + string(tier) + " open *"} {
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
