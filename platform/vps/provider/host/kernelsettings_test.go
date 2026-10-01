package host

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func applyReading(t *testing.T, kernel string) []string {
	t.Helper()
	tier := environment.TierProduction
	box := bootstrappedOn(t, tier)
	box.answer = func(command string) (session.Result, bool) {
		switch command {
		case kernelSettingsCommand:
			return session.Result{Stdout: kernel}, true
		case "cat ~/.ssh/authorized_keys 2>/dev/null":
			return session.Result{Stdout: aKey + "\n"}, true
		}
		return session.Result{}, false
	}
	progress := &fake.Log{}
	if err := NewBootstrap(box.host(), testVendor, "shop").Apply(context.Background(),
		provider.BootstrapRequest{Tier: tier, WrittenBy: "the-suite"}, progress); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	var warned []string
	for _, line := range progress.Lines() {
		if strings.HasPrefix(line, "WARN ") {
			warned = append(warned, line)
		}
	}
	return warned
}

func TestAnApplyWarnsOfTheKernelSettingsValkeyLogsAgainst(t *testing.T) {
	t.Parallel()

	warned := applyReading(t, "0\nalways [madvise] never\n")
	if len(warned) != 1 || !strings.Contains(warned[0], "vm.overcommit_memory is 0") || !strings.Contains(warned[0], "sysctl vm.overcommit_memory=1") {
		t.Errorf("warned %q, want overcommit 0 named with the setting that fixes it", warned)
	}

	warned = applyReading(t, "1\n[always] madvise never\n")
	if len(warned) != 1 || !strings.Contains(warned[0], "transparent huge pages") || !strings.Contains(warned[0], "madvise") {
		t.Errorf("warned %q, want transparent huge pages set to always named with the setting that fixes it", warned)
	}
}

func TestAnApplyOverAKernelValkeyIsContentWithWarnsOfNothing(t *testing.T) {
	t.Parallel()

	if warned := applyReading(t, "1\nalways [madvise] never\n"); len(warned) != 0 {
		t.Errorf("warned %q, want nothing", warned)
	}
	if warned := applyReading(t, ""); len(warned) != 0 {
		t.Errorf("warned %q over a kernel that exposes neither setting, want nothing", warned)
	}
}

func TestKernelWarningsReadBothSettings(t *testing.T) {
	t.Parallel()

	if got := kernelWarnings("2\n[always] madvise never\n"); len(got) != 2 || !slices.ContainsFunc(got, func(line string) bool { return strings.Contains(line, "is 2") }) {
		t.Errorf("kernelWarnings() = %q, want both settings named", got)
	}
}
