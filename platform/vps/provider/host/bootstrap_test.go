package host

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func standingHost(t *testing.T) Reading {
	t.Helper()

	class := providerkit.ClassProduction
	keys := []byte(aKey + "\n")
	return Reading{
		Class:    class,
		Present:  true,
		Keys:     keys,
		Stamp:    Stamp{Schema: providerkit.BootstrapSchema, State: StateComplete, Digests: digests(Items(class, keys))},
		Observed: digests(Items(class, keys)),
	}
}

func drifted(t *testing.T, read Reading, name string) Reading {
	t.Helper()

	for _, item := range Items(read.Class, read.Keys) {
		if item.Name != name {
			continue
		}
		read.Observed[item.ID()] = "a digest nothing this ocel writes produces"
		return read
	}
	t.Fatalf("nothing in the item set is named %s", name)
	return read
}

func refusal(t *testing.T, err error, code providerkit.Code) providerkit.Refusal {
	t.Helper()

	var refused providerkit.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("error = %v, want a refusal the CLI can render", err)
	}
	if refused.Code != code {
		t.Fatalf("refusal code = %q, want %q", refused.Code, code)
	}
	return refused
}

func TestAnUnattendedApplyInstallsWhatIsAbsent(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	fresh := Reading{Class: class, Observed: map[string]string{}}
	if err := admitReplacements(fresh, Items(class, nil)); err != nil {
		t.Errorf("an unattended apply over a machine nothing has bootstrapped = %v, want the installs to proceed", err)
	}
}

func TestAnUnattendedApplyWillNotWriteOverWhatAlreadyStands(t *testing.T) {
	t.Parallel()

	read := drifted(t, standingHost(t), recordsHelper)
	refused := refusal(t, admitReplacements(read, Items(read.Class, read.Keys)), providerkit.CodeNotReady)
	if !strings.Contains(refused.Message, recordsHelper) {
		t.Errorf("the refusal says %q, want it to name %s as the thing it would write over", refused.Message, recordsHelper)
	}
	if !strings.Contains(refused.Message, "--yes") {
		t.Errorf("the refusal says %q, want it to name the flag that accepts the write", refused.Message)
	}
}
