package host

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestEachClassIsWhitelistedOnItsOwnSudoersLineNamingItsClassAlone(t *testing.T) {
	t.Parallel()

	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		fragment := written(Items(class, []byte(aKey+"\n"), ArchAMD64), KindFile, sudoersSeal(class))
		if fragment.Name == "" {
			t.Fatalf("bootstrapping %s writes no sudoers line of its own", class)
		}
		line := string(fragment.Content)
		other := string(providerkit.ClassProduction)
		if class == providerkit.ClassProduction {
			other = string(providerkit.ClassPreview)
		}
		for _, banned := range []string{" " + other + " ", " init", SealHelper + ","} {
			if strings.Contains(line, banned) {
				t.Errorf("%s's line reads %q and carries %q, which lets the deploy login past the class or the verbs it needs", class, line, banned)
			}
		}
		for _, wanted := range []string{SealHelper + " " + string(class) + " seal *", SealHelper + " " + string(class) + " open *"} {
			if !strings.Contains(line, wanted) {
				t.Errorf("%s's line reads %q and never grants %q", class, line, wanted)
			}
		}
		granted := strings.TrimPrefix(strings.TrimSpace(line), deployUser+" ALL=(root) NOPASSWD: ")
		for _, allowed := range strings.Split(granted, ", ") {
			if strings.ContainsAny(allowed, ":=\\") {
				t.Errorf("%s's line grants %q, and sudoers reads an unescaped ':', '=' or '\\' inside a command's arguments as syntax", class, allowed)
			}
		}
	}
}
