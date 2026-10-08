package projectinit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
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

	code, hint := runFailingInit(t, dir, initOptions{provider: twoOptionProvider})

	if code != clierror.CodeInputRequired || hint != "--option host=<value> --option zone=<value>" {
		t.Errorf("code, hint = %q, %q; want input_required naming both host and zone", code, hint)
	}
}

func TestAnOptionValueIsWrittenAsTheLiteralTheConfigFormatReadsBack(t *testing.T) {
	value := `bo"x\ ${HOME} é`
	for _, format := range []string{"json", "yaml", "ts"} {
		t.Run(format, func(t *testing.T) {
			dependencies := newTestDependencies()
			stubPackageManager(&dependencies, nil)
			dir := initTestDir(t, "proj")
			if format == "ts" {
				if _, err := exec.LookPath("node"); err != nil {
					t.Skip("node not found on PATH")
				}
				installVPSProviderPackage(t, dir)
			}

			opts := initOptions{provider: "vps", format: format, settings: []providerSetting{{name: "ssh", value: value}}}
			if _, err := runInit(context.Background(), dependencies, dir, "my-app", opts); err != nil {
				t.Fatalf("runInit err = %v", err)
			}

			cfg, err := project.Load(context.Background(), dir, "")
			if err != nil {
				t.Fatalf("the config init wrote does not load: %v", err)
			}
			var options struct {
				SSH string `json:"ssh"`
			}
			if err := json.Unmarshal(cfg.Provider.Options, &options); err != nil || options.SSH != value {
				t.Errorf("ssh = %q (err %v), want %q", options.SSH, err, value)
			}
		})
	}
}

func installVPSProviderPackage(t *testing.T, dir string) {
	t.Helper()
	pkg := filepath.Join(dir, "node_modules", "ocel")
	clitest.WriteFile(t, filepath.Join(pkg, "package.json"), `{"name":"ocel","type":"module","exports":{"./config":"./config.js","./providers/vps":"./vps.js"}}`)
	clitest.WriteFile(t, filepath.Join(pkg, "config.js"), `export const defineConfig = (config) => config;`)
	clitest.WriteFile(t, filepath.Join(pkg, "vps.js"), `export default (options) => ({ vps: options });`)
}

func TestInitAsksOnATerminalForTheRequiredOptionsItWasNotGivenAndLeavesStdoutToTheResult(t *testing.T) {
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)
	dependencies.StdinIsTerminal = func(io.Reader) bool { return true }
	dir := initTestDir(t, "proj")

	var prompts, stdout bytes.Buffer
	opts := initOptions{provider: "vps"}
	err := runInitCommand(context.Background(), dependencies, dir, "my-app", opts, strings.NewReader("203.0.113.7\n"), &prompts, &stdout)
	if err != nil {
		t.Fatalf("runInitCommand err = %v", err)
	}
	if got := readConfig(t, dir); !strings.Contains(got, `vpsProvider({ "ssh": "203.0.113.7" })`) {
		t.Errorf("config = %s, want the ssh target typed", got)
	}
	if !strings.Contains(prompts.String(), "ssh") {
		t.Errorf("prompts = %q, want the question for ssh", prompts.String())
	}
	if strings.Contains(stdout.String(), "ssh") {
		t.Errorf("stdout = %q, want the question kept off it", stdout.String())
	}
}

func TestInitDoesNotAskForAnOptionAFlagAlreadyGave(t *testing.T) {
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)
	dependencies.StdinIsTerminal = func(io.Reader) bool { return true }
	dir := initTestDir(t, "proj")

	var prompts bytes.Buffer
	opts := initOptions{provider: "vps", settings: []providerSetting{{name: "ssh", value: "box"}}}
	if err := runInitCommand(context.Background(), dependencies, dir, "my-app", opts, strings.NewReader(""), &prompts, io.Discard); err != nil {
		t.Fatalf("runInitCommand err = %v", err)
	}
	if prompts.Len() > 0 {
		t.Errorf("asked %q, want no question", prompts.String())
	}
}

func TestInitRefusesAnExistingConfigBeforeAskingForAnOption(t *testing.T) {
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)
	dependencies.StdinIsTerminal = func(io.Reader) bool { return true }
	dir := initTestDir(t, "proj")
	if err := os.WriteFile(filepath.Join(dir, project.DefaultFileName), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var prompts bytes.Buffer
	err := runInitCommand(context.Background(), dependencies, dir, "my-app", initOptions{provider: "vps"}, strings.NewReader("box\n"), &prompts, io.Discard)

	if code := clierror.NewRunError(err).GetCode(); code != clierror.CodeInitConfigExists {
		t.Fatalf("code = %q (err %v), want %s", code, err, clierror.CodeInitConfigExists)
	}
	if prompts.Len() > 0 {
		t.Errorf("asked %q before refusing, want no question", prompts.String())
	}
}

func TestInitUnderJSONFailsInsteadOfAskingForARequiredOption(t *testing.T) {
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)
	dependencies.StdinIsTerminal = func(io.Reader) bool { return true }
	dependencies.IsJSON = func() bool { return true }
	dir := initTestDir(t, "proj")

	err := runInitCommand(context.Background(), dependencies, dir, "my-app", initOptions{provider: "vps"}, strings.NewReader("box\n"), io.Discard, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "--option") {
		t.Fatalf("err = %v, want input_required naming --option", err)
	}
}

func TestInitRefusesAnOptionTheProviderDoesNotTakeAndNamesTheOnesItDoes(t *testing.T) {
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)
	dir := initTestDir(t, "proj")

	opts := initOptions{provider: "vps", settings: []providerSetting{{name: "ssh", value: "box"}, {name: "foo", value: "bar"}}}
	_, err := runInit(context.Background(), dependencies, dir, "my-app", opts)

	if err == nil || !strings.Contains(err.Error(), `"foo"`) || !strings.Contains(err.Error(), "only deployKey, proxy and ssh") {
		t.Fatalf("err = %v, want it to name foo and every option vps takes", err)
	}
	if _, err := os.Stat(filepath.Join(dir, project.TSFileName)); err == nil {
		t.Error("a refused init wrote a config")
	}
}

func TestInitTakesAnOptionTheProviderDoesNotRequire(t *testing.T) {
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)
	dir := initTestDir(t, "proj")

	opts := initOptions{provider: "gcp", settings: []providerSetting{{name: "region", value: "europe-west1"}, {name: "project", value: "acme-prod"}}}
	if _, err := runInit(context.Background(), dependencies, dir, "my-app", opts); err != nil {
		t.Fatalf("runInit err = %v", err)
	}
	if got := readConfig(t, dir); !strings.Contains(got, `"project": "acme-prod"`) {
		t.Errorf("config = %s, want the project it was given", got)
	}
}

func TestInitCountsABlankOptionAsMissing(t *testing.T) {
	dir := initTestDir(t, "proj")

	code, hint := runFailingInit(t, dir, initOptions{provider: "vps", settings: []providerSetting{{name: "ssh", value: "  "}}})

	if code != clierror.CodeInputRequired || !strings.Contains(hint, "--option ssh=") {
		t.Fatalf("code, hint = %q, %q; want input_required naming --option ssh=<value>", code, hint)
	}
}

func TestInitCountsABlankAnswerAsMissing(t *testing.T) {
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)
	dependencies.StdinIsTerminal = func(io.Reader) bool { return true }
	dir := initTestDir(t, "proj")

	err := runInitCommand(context.Background(), dependencies, dir, "my-app", initOptions{provider: "vps"}, strings.NewReader("   \n"), io.Discard, io.Discard)

	if code := clierror.NewRunError(err).GetCode(); code != clierror.CodeInputRequired {
		t.Fatalf("code = %q (err %v), want %s", code, err, clierror.CodeInputRequired)
	}
}

func TestInitStopsAsInterruptedWhenTheOptionQuestionIsCancelled(t *testing.T) {
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)
	dependencies.StdinIsTerminal = func(io.Reader) bool { return true }
	dir := initTestDir(t, "proj")

	err := runInitCommand(context.Background(), dependencies, dir, "my-app", initOptions{provider: "vps"}, strings.NewReader(""), io.Discard, io.Discard)

	if code := clierror.NewRunError(err).GetCode(); code != clierror.CodeInterrupted {
		t.Fatalf("code = %q (err %v), want %s", code, err, clierror.CodeInterrupted)
	}
	if _, err := os.Stat(filepath.Join(dir, project.TSFileName)); err == nil {
		t.Error("a cancelled init wrote a config")
	}
}

type writesConfigWhenRead struct {
	path    string
	answer  io.Reader
	written bool
}

func (r *writesConfigWhenRead) Read(p []byte) (int, error) {
	if !r.written {
		r.written = true
		if err := os.WriteFile(r.path, []byte("theirs\n"), 0o644); err != nil {
			return 0, err
		}
	}
	return r.answer.Read(p)
}

func TestInitRefusesAConfigWrittenWhileItWasAskingForAnOption(t *testing.T) {
	dependencies := newTestDependencies()
	stubPackageManager(&dependencies, nil)
	dependencies.StdinIsTerminal = func(io.Reader) bool { return true }
	dir := initTestDir(t, "proj")
	configPath := filepath.Join(dir, project.TSFileName)
	stdin := &writesConfigWhenRead{path: configPath, answer: strings.NewReader("box\n")}

	err := runInitCommand(context.Background(), dependencies, dir, "my-app", initOptions{provider: "vps"}, stdin, io.Discard, io.Discard)

	if code := clierror.NewRunError(err).GetCode(); code != clierror.CodeInitConfigExists {
		t.Fatalf("code = %q (err %v), want %s", code, err, clierror.CodeInitConfigExists)
	}
	if got, _ := os.ReadFile(configPath); string(got) != "theirs\n" {
		t.Errorf("config = %q, want the one written while init asked left as it was", got)
	}
}
