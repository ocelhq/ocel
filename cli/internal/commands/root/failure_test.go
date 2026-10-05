package root

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func executeAndReportRoot(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	ocel := newCommand()
	ocel.root.SetOut(&out)
	ocel.root.SetErr(&errOut)
	code = ocel.executeAndReport(args)
	return code, out.String(), errOut.String()
}

func requireOneFailureDocument(t *testing.T, stdout string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(stdout))
	var doc map[string]any
	if err := decoder.Decode(&doc); err != nil {
		t.Fatalf("stdout = %q, want one JSON document: %v", stdout, err)
	}
	if decoder.More() {
		t.Fatalf("stdout = %q, want exactly one JSON document", stdout)
	}
	if doc["ok"] != false {
		t.Fatalf("document = %v, want ok:false", doc)
	}
	failure, ok := doc["error"].(map[string]any)
	if !ok {
		t.Fatalf("document = %v, want an error object", doc)
	}
	return failure
}

func TestAnUnknownFlagUnderJSONPrintsOneUsageDocumentOnStdout(t *testing.T) {
	code, stdout, stderr := executeAndReportRoot(t, "--json", "deploy", "--no-such-flag")

	failure := requireOneFailureDocument(t, stdout)
	if failure["code"] != "usage" {
		t.Errorf("error code = %v, want usage", failure["code"])
	}
	if message, _ := failure["message"].(string); !strings.Contains(message, "unknown flag: --no-such-flag") {
		t.Errorf("error message = %q, want the unknown flag named", message)
	}
	if hint, _ := failure["hint"].(string); !strings.HasPrefix(hint, "ocel deploy") {
		t.Errorf("error hint = %q, want the usage line of ocel deploy", hint)
	}
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing beside the one document", stderr)
	}
}

func TestAnUnknownFlagBeforeTheJSONFlagStillPrintsTheUsageDocument(t *testing.T) {
	_, stdout, stderr := executeAndReportRoot(t, "deploy", "--no-such-flag", "--json")

	if failure := requireOneFailureDocument(t, stdout); failure["code"] != "usage" {
		t.Errorf("error code = %v, want usage", failure["code"])
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing", stderr)
	}
}

func TestOCELJSONMakesAUsageErrorADocument(t *testing.T) {
	t.Setenv("OCEL_JSON", "1")

	_, stdout, _ := executeAndReportRoot(t, "deploy", "--no-such-flag")

	if failure := requireOneFailureDocument(t, stdout); failure["code"] != "usage" {
		t.Errorf("error code = %v, want usage", failure["code"])
	}
}

func TestAWrongArgumentCountUnderJSONPrintsAUsageDocument(t *testing.T) {
	code, stdout, stderr := executeAndReportRoot(t, "--json", "logout", "extra")

	failure := requireOneFailureDocument(t, stdout)
	if failure["code"] != "usage" {
		t.Errorf("error code = %v, want usage", failure["code"])
	}
	if hint, _ := failure["hint"].(string); !strings.HasPrefix(hint, "ocel logout") {
		t.Errorf("error hint = %q, want the usage line of ocel logout", hint)
	}
	if code != 1 || stderr != "" {
		t.Errorf("exit code = %d, stderr = %q, want 1 and nothing", code, stderr)
	}
}

func TestAnUnknownCommandUnderJSONPrintsAUsageDocument(t *testing.T) {
	_, stdout, stderr := executeAndReportRoot(t, "--json", "no-such-command")

	failure := requireOneFailureDocument(t, stdout)
	if failure["code"] != "usage" {
		t.Errorf("error code = %v, want usage", failure["code"])
	}
	if hint, _ := failure["hint"].(string); !strings.HasPrefix(hint, "ocel") {
		t.Errorf("error hint = %q, want the usage line of ocel", hint)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing", stderr)
	}
}

func TestACommandThatNeverStartsARunPrintsItsFailureAsOneCodedDocument(t *testing.T) {
	for _, command := range []string{"lock", "generate"} {
		t.Run(command, func(t *testing.T) {
			t.Chdir(t.TempDir())

			code, stdout, stderr := executeAndReportRoot(t, "--json", command)

			failure := requireOneFailureDocument(t, stdout)
			if failure["code"] != "project.no_config" || failure["hint"] != "ocel init" {
				t.Errorf("error code = %v, hint = %v, want project.no_config and ocel init", failure["code"], failure["hint"])
			}
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want nothing beside the one document", stderr)
			}
		})
	}
}

func TestAFailureThatAlreadyEndedInARunSummaryPrintsNothingMore(t *testing.T) {
	ocel := newCommand()
	ocel.root.AddCommand(&cobra.Command{
		Use: "fail",
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			_, running, err := ocel.bus.Begin(cmd.Context(), "ocel fail", "")
			if err != nil {
				return err
			}
			defer running.End(&err)
			return errors.New("the upload was refused")
		},
	})
	var stdout, stderr bytes.Buffer
	ocel.root.SetOut(&stdout)
	ocel.root.SetErr(&stderr)

	code := ocel.executeAndReport([]string{"--json", "fail"})

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	var last map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("last line %q is not JSON: %v", lines[len(lines)-1], err)
	}
	if _, ok := last["summary"]; !ok {
		t.Errorf("last line = %v, want the run's summary and nothing after it", last)
	}
	for _, line := range lines {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %q is not JSON: %v", line, err)
		}
		if _, ok := ev["ok"]; ok {
			t.Errorf("line %q is an error document, want the summary to be the only report", line)
		}
	}
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", stderr.String())
	}
}

func TestAProjectThatFailsToLoadBeforeARunStartsPrintsOneErrorDocument(t *testing.T) {
	t.Chdir(t.TempDir())

	code, stdout, stderr := executeAndReportRoot(t, "--json", "deploy")

	failure := requireOneFailureDocument(t, stdout)
	if message, _ := failure["message"].(string); message == "" {
		t.Errorf("error = %v, want the load failure's message", failure)
	}
	if code != 1 || stderr != "" {
		t.Errorf("exit code = %d, stderr = %q, want 1 and nothing", code, stderr)
	}
}

func TestAnOCELJSONThatIsNotABooleanStillAsksForTheFailureAsADocument(t *testing.T) {
	t.Setenv("OCEL_JSON", "garbage")

	code, stdout, stderr := executeAndReportRoot(t, "deployments", "ls")

	failure := requireOneFailureDocument(t, stdout)
	if message, _ := failure["message"].(string); !strings.HasPrefix(message, "OCEL_JSON") {
		t.Errorf("error message = %q, want the invalid OCEL_JSON named", message)
	}
	if code != 1 || stderr != "" {
		t.Errorf("exit code = %d, stderr = %q, want 1 and nothing", code, stderr)
	}
}

func TestAJSONFlagThatIsNotABooleanStillAsksForTheUsageDocument(t *testing.T) {
	code, stdout, stderr := executeAndReportRoot(t, "deployments", "ls", "--json=garbage")

	failure := requireOneFailureDocument(t, stdout)
	if failure["code"] != "usage" {
		t.Errorf("error code = %v, want usage", failure["code"])
	}
	if code != 1 || stderr != "" {
		t.Errorf("exit code = %d, stderr = %q, want 1 and nothing", code, stderr)
	}
}

func TestAnExplicitlyFalseJSONFlagOverridesAnInvalidOCELJSON(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("OCEL_JSON", "garbage")

	code, stdout, stderr := executeAndReportRoot(t, "--json=false", "deploy", "--no-such-flag")

	if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "✗ ") {
		t.Errorf("code = %d, stdout = %q, stderr = %q, want the human failure block on stderr", code, stdout, stderr)
	}
}

func TestTheHumanViewPrintsAFailureAsTheFailureBlockOnStderrOnly(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Chdir(t.TempDir())

	for name, args := range map[string][]string{
		"a usage error":    {"deploy", "--no-such-flag"},
		"a failed command": {"lock"},
		"json off":         {"--json=false", "lock"},
	} {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := executeAndReportRoot(t, args...)

			if code != 1 || stdout != "" {
				t.Errorf("code = %d, stdout = %q, want 1 and nothing on stdout", code, stdout)
			}
			if !strings.HasPrefix(stderr, "✗ ") {
				t.Errorf("stderr = %q, want the human failure block", stderr)
			}
		})
	}
}

func TestAUsageErrorsHumanTextIsTheCobraMessage(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	_, _, stderr := executeAndReportRoot(t, "deploy", "--no-such-flag")

	if want := "✗ unknown flag: --no-such-flag\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
}

func TestASuccessfulCommandUnderJSONExitsZeroAndPrintsNoFailure(t *testing.T) {
	code, stdout, stderr := executeAndReportRoot(t, "--json", "--version")

	if code != 0 || stderr != "" || strings.Contains(stdout, `"ok"`) {
		t.Errorf("code = %d, stdout = %q, stderr = %q, want a clean exit", code, stdout, stderr)
	}
}

func TestTheUsageDocumentCarriesTheStableCodeAndItsDocsURL(t *testing.T) {
	_, stdout, _ := executeAndReportRoot(t, "--json", "deploy", "--no-such-flag")

	failure := requireOneFailureDocument(t, stdout)
	if got := failure["docsUrl"]; got != "https://ocel.dev/docs/errors/usage" {
		t.Errorf("docsUrl = %v, want the usage error page", got)
	}
	if got, want := slices.Sorted(maps.Keys(failure)), []string{"code", "docsUrl", "hint", "message"}; !slices.Equal(got, want) {
		t.Errorf("error keys = %v, want %v", got, want)
	}
}

func TestMutuallyExclusiveFlagsUnderJSONPrintAUsageDocument(t *testing.T) {
	t.Chdir(t.TempDir())

	code, stdout, stderr := executeAndReportRoot(t, "--json", "init", "--ts", "--yaml")

	failure := requireOneFailureDocument(t, stdout)
	if failure["code"] != "usage" {
		t.Errorf("error code = %v, want usage", failure["code"])
	}
	if hint, _ := failure["hint"].(string); !strings.HasPrefix(hint, "ocel init") {
		t.Errorf("error hint = %q, want the usage line of ocel init", hint)
	}
	if code != 1 || stderr != "" {
		t.Errorf("exit code = %d, stderr = %q, want 1 and nothing", code, stderr)
	}
}

func TestAMissingRequiredFlagUnderJSONPrintsAUsageDocumentWithoutRunningTheCommand(t *testing.T) {
	ocel := newCommand()
	ran := false
	needs := &cobra.Command{Use: "needs", RunE: func(*cobra.Command, []string) error { ran = true; return nil }}
	needs.Flags().String("name", "", "")
	if err := needs.MarkFlagRequired("name"); err != nil {
		t.Fatal(err)
	}
	ocel.root.AddCommand(needs)
	var stdout, stderr bytes.Buffer
	ocel.root.SetOut(&stdout)
	ocel.root.SetErr(&stderr)

	code := ocel.executeAndReport([]string{"--json", "needs"})

	failure := requireOneFailureDocument(t, stdout.String())
	if failure["code"] != "usage" {
		t.Errorf("error code = %v, want usage", failure["code"])
	}
	if hint, _ := failure["hint"].(string); !strings.HasPrefix(hint, "ocel needs") {
		t.Errorf("error hint = %q, want the usage line of ocel needs", hint)
	}
	if code != 1 || stderr.Len() != 0 || ran {
		t.Errorf("exit code = %d, stderr = %q, ran = %v, want 1, nothing, and the command not run", code, stderr.String(), ran)
	}
}

func executeOnATerminal(t *testing.T, args ...string) (code int, stdout string) {
	t.Helper()
	tty, screen := aTerminal(t, "xterm", 80)
	var out bytes.Buffer
	ocel := newCommand()
	ocel.root.SetIn(tty)
	ocel.root.SetOut(&out)
	ocel.root.SetErr(&bytes.Buffer{})
	finished := make(chan int, 1)
	go func() { finished <- ocel.executeAndReport(args) }()
	select {
	case code = <-finished:
	case <-time.After(20 * time.Second):
		t.Fatalf("ocel %s is waiting for an answer on the terminal", strings.Join(args, " "))
	}
	if asked := screen(); strings.TrimSpace(asked) != "" {
		t.Errorf("terminal = %q, want nothing asked under --json", asked)
	}
	return code, out.String()
}

func TestAMissingProjectUnderJSONOnATerminalFailsInsteadOfOfferingASetup(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		args []string
	}{
		{"--json", "", []string{"--json", "deploy"}},
		{"OCEL_JSON=1", "1", []string{"deploy"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("OCEL_JSON", tc.env)

			code, stdout := executeOnATerminal(t, tc.args...)

			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if strings.Contains(stdout, "Set up a project here?") {
				t.Errorf("stdout = %q, want no prompt text in the JSON stream", stdout)
			}
			if failure := requireOneFailureDocument(t, stdout); failure["code"] != "project.no_config" {
				t.Errorf("error = %v, want project.no_config", failure)
			}
		})
	}
}
