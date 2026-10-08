package projectinit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/configdoc"
)

func TestOptionFlagsAreNameValuePairsTheProviderIsGiven(t *testing.T) {
	got, err := parseOptionFlags([]string{"ssh=box", "region=us-east1=a"})
	if err != nil {
		t.Fatalf("parseOptionFlags err = %v", err)
	}
	want := []providerSetting{{name: "ssh", value: "box"}, {name: "region", value: "us-east1=a"}}
	if !slices.Equal(got, want) {
		t.Errorf("settings = %v, want %v", got, want)
	}
}

func TestAnOptionFlagThatIsNotANameValuePairOrRepeatsANameIsRefused(t *testing.T) {
	for name, flags := range map[string][]string{
		"no value":     {"ssh"},
		"no name":      {"=box"},
		"repeats name": {"ssh=a", "ssh=b"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseOptionFlags(flags)
			if err == nil || !strings.Contains(err.Error(), "--option") {
				t.Fatalf("err = %v, want it to name --option", err)
			}
		})
	}
}

func TestInitRefusesAProviderWhoseRequiredOptionsAreNotGivenAndWritesNothing(t *testing.T) {
	dependencies := newTestDependencies()
	argv := stubPackageManager(&dependencies, nil)
	dir := initTestDir(t, "proj")

	code, hint := runFailingInit(t, dir, initOptions{provider: "vps"})

	if code != "input_required" || !strings.Contains(hint, "--option ssh=") {
		t.Fatalf("code, hint = %q, %q; want input_required naming --option ssh=<value>", code, hint)
	}
	if _, err := os.Stat(filepath.Join(dir, project.TSFileName)); err == nil {
		t.Error("a refused init wrote a config")
	}
	if *argv != nil {
		t.Errorf("ran %v, want no package manager call", *argv)
	}
}

func TestInitNamesEveryRequiredOptionThatIsMissing(t *testing.T) {
	dir := initTestDir(t, "proj")

	_, hint := runFailingInit(t, dir, initOptions{provider: "gcp"})

	if !strings.Contains(hint, "--option region=") {
		t.Errorf("hint = %q, want it to name region", hint)
	}
}

func TestAnOptionValueIsWrittenAsTheLiteralTheConfigFormatReadsBack(t *testing.T) {
	value := `bo"x\ ${HOME} é`
	for _, format := range []string{"json", "yaml", "ts"} {
		t.Run(format, func(t *testing.T) {
			dependencies := newTestDependencies()
			stubPackageManager(&dependencies, nil)
			dir := initTestDir(t, "proj")

			opts := initOptions{provider: "vps", format: format, settings: []providerSetting{{name: "ssh", value: value}}}
			if _, err := runInit(context.Background(), dependencies, dir, "my-app", opts); err != nil {
				t.Fatalf("runInit err = %v", err)
			}

			written, err := os.ReadFile(filepath.Join(dir, configFileName(opts, sdkLanguage{})))
			if err != nil {
				t.Fatalf("read config: %v", err)
			}
			if format != "json" {
				if !strings.Contains(string(written), `"bo\"x\\ $${HOME} é"`) {
					t.Errorf("config = %s, want the value as an escaped literal", written)
				}
				return
			}
			doc, err := configdoc.Decode(written, func(string) (string, bool) { return "", false })
			if err != nil {
				t.Fatalf("the config init wrote does not load: %v\n%s", err, written)
			}
			var options struct {
				SSH string `json:"ssh"`
			}
			if err := json.Unmarshal(doc.Provider.Options, &options); err != nil || options.SSH != value {
				t.Errorf("ssh = %q (err %v), want %q", options.SSH, err, value)
			}
		})
	}
}

func TestInitAsksOnATerminalForTheRequiredOptionsItWasNotGiven(t *testing.T) {
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)
	dependencies.StdinIsTerminal = func(io.Reader) bool { return true }
	dir := initTestDir(t, "proj")

	var out bytes.Buffer
	opts := initOptions{provider: "vps"}
	err := runInitCommand(context.Background(), dependencies, dir, "my-app", opts, strings.NewReader("203.0.113.7\n"), &out)
	if err != nil {
		t.Fatalf("runInitCommand err = %v", err)
	}
	if got := readConfig(t, dir); !strings.Contains(got, `vpsProvider({ "ssh": "203.0.113.7" })`) {
		t.Errorf("config = %s, want the ssh target typed", got)
	}
}

func TestInitDoesNotAskForAnOptionAFlagAlreadyGave(t *testing.T) {
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)
	dependencies.StdinIsTerminal = func(io.Reader) bool { return true }
	dir := initTestDir(t, "proj")

	var out bytes.Buffer
	opts := initOptions{provider: "vps", settings: []providerSetting{{name: "ssh", value: "box"}}}
	if err := runInitCommand(context.Background(), dependencies, dir, "my-app", opts, strings.NewReader(""), &out); err != nil {
		t.Fatalf("runInitCommand err = %v", err)
	}
	if strings.Contains(out.String(), "ssh") {
		t.Errorf("asked %q, want no question", out.String())
	}
}

func TestInitUnderJSONFailsInsteadOfAskingForARequiredOption(t *testing.T) {
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)
	dependencies.StdinIsTerminal = func(io.Reader) bool { return true }
	dependencies.IsJSON = func() bool { return true }
	dir := initTestDir(t, "proj")

	err := runInitCommand(context.Background(), dependencies, dir, "my-app", initOptions{provider: "vps"}, strings.NewReader("box\n"), io.Discard)

	if err == nil || !strings.Contains(err.Error(), "--option") {
		t.Fatalf("err = %v, want input_required naming --option", err)
	}
}
