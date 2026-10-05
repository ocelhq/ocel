package root

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func executeUnderJSONOnATerminal(t *testing.T, args ...string) (code int, stdout string) {
	t.Helper()
	tty, screen := aTerminal(t, "xterm", 80)
	var out bytes.Buffer
	ocel := newCommand()
	ocel.root.SetIn(tty)
	ocel.root.SetOut(&out)
	ocel.root.SetErr(&bytes.Buffer{})
	finished := make(chan int, 1)
	go func() { finished <- ocel.executeAndReport(append([]string{"--json"}, args...)) }()
	select {
	case code = <-finished:
	case <-time.After(20 * time.Second):
		t.Fatalf("ocel --json %s is waiting for an answer on the terminal", strings.Join(args, " "))
	}
	if asked := screen(); strings.TrimSpace(asked) != "" {
		t.Errorf("terminal = %q, want nothing asked under --json", asked)
	}
	return code, out.String()
}

func TestAMissingProjectUnderJSONOnATerminalFailsInsteadOfOfferingASetup(t *testing.T) {
	t.Chdir(t.TempDir())

	code, stdout := executeUnderJSONOnATerminal(t, "deploy")

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if strings.Contains(stdout, "Set up a project here?") {
		t.Errorf("stdout = %q, want no prompt text in the JSON stream", stdout)
	}
	if failure := requireOneFailureDocument(t, stdout); failure["code"] != "project.no_config" {
		t.Errorf("error = %v, want project.no_config", failure)
	}
}
