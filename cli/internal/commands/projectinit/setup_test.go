package projectinit

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/configdoc"
)

func missingConfigIn(t *testing.T, dir string) prerequisite.MissingError {
	t.Helper()
	_, err := project.Load(context.Background(), dir, "")
	var missing prerequisite.MissingError
	if !errors.As(err, &missing) {
		t.Fatalf("Load err = %v, want the project missing", err)
	}
	return missing
}

func choiceOf(t *testing.T, provider string) string {
	t.Helper()
	i := slices.Index(configdoc.ProviderIDs(), provider)
	if i < 0 {
		t.Fatalf("ocel ships no %s provider", provider)
	}
	return strconv.Itoa(i + 1)
}

func TestTheInitSetupAsksForTheProviderAndWhatItRequiresThenWritesTheConfig(t *testing.T) {
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)
	dir := initTestDir(t, "shop")

	var out bytes.Buffer
	policy := consent.Policy{Interactive: true, In: strings.NewReader(choiceOf(t, "vps") + "\n203.0.113.7\n"), Out: &out}
	if err := NewSetup(dependencies).Run(context.Background(), policy, run.NewBus(time.Now).Preamble(context.Background()), missingConfigIn(t, dir)); err != nil {
		t.Fatalf("Run err = %v, want the project initialized", err)
	}
	written, err := os.ReadFile(filepath.Join(dir, project.TSFileName))
	if err != nil {
		t.Fatalf("read the config init wrote: %v", err)
	}
	if !strings.Contains(string(written), `provider: vpsProvider({ "ssh": "203.0.113.7" }),`) {
		t.Errorf("config = %s, want the provider chosen with the ssh target typed", written)
	}
	if !strings.Contains(out.String(), "Where should shop deploy?") {
		t.Errorf("asked %q, want the provider question to name the project", out.String())
	}
}

func TestAnInitSetupLeftUnansweredIsADecline(t *testing.T) {
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)
	dir := initTestDir(t, "shop")

	policy := consent.Policy{Interactive: true, In: strings.NewReader(""), Out: &bytes.Buffer{}}
	err := NewSetup(dependencies).Run(context.Background(), policy, run.NewBus(time.Now).Preamble(context.Background()), missingConfigIn(t, dir))
	if !prerequisite.IsDeclined(err) {
		t.Errorf("Run err = %v, want a decline", err)
	}
	if _, err := os.Stat(filepath.Join(dir, project.TSFileName)); err == nil {
		t.Error("an unanswered setup wrote a config")
	}
}

func TestEveryConfigFormatCarriesTheProviderSettingsInitAskedFor(t *testing.T) {
	settings := []providerSetting{{name: "ssh", value: "203.0.113.7"}}
	for name, want := range map[string]string{
		project.YAMLFileName: "provider:\n  vps:\n    ssh: \"203.0.113.7\"\n",
		project.TSFileName:   `provider: vpsProvider({ "ssh": "203.0.113.7" }),`,
	} {
		if got := configTemplate(name, "shop", "vps", settings); !strings.Contains(got, want) {
			t.Errorf("%s = %s, want it to contain %q", name, got, want)
		}
	}
}
