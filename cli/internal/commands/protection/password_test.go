package protection

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
)

func TestPasswordRevealsThePasswordOnStdoutAlone(t *testing.T) {
	project := protectedProject(t)
	if _, _, err := executePreview(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}
	want := recorded(t, project).Password

	stdout, stderr, err := executePreview(t, project, "", "password")

	if err != nil {
		t.Fatalf("password: %v; stderr=%s", err, stderr)
	}
	if strings.TrimSpace(stdout) != want || strings.Contains(stderr, want) {
		t.Errorf("stdout = %q stderr = %q, want the password on stdout alone", stdout, stderr)
	}
}

func TestPasswordBeforeAnyPreviewDeployedSaysTheFirstDeployCreatesIt(t *testing.T) {
	project := protectedProject(t)

	stdout, _, err := executePreview(t, project, "", "password")

	if err == nil || !strings.Contains(err.Error(), "first preview deploy") {
		t.Errorf("password: err = %v, want it to say the first preview deploy creates the password", err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
}

func TestRotatePrintsOnlyTheNewPasswordAndEndsEverySession(t *testing.T) {
	project := protectedProject(t)
	if _, _, err := executePreview(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}
	before := recorded(t, project)

	stdout, stderr, err := executePreview(t, project, "", "password", "rotate")

	if err != nil {
		t.Fatalf("rotate: %v; stderr=%s", err, stderr)
	}
	password := strings.TrimSpace(stdout)
	after := recorded(t, project)
	if password == "" || strings.ContainsAny(password, " \n") || password == before.Password || after.Password != password {
		t.Fatalf("stdout = %q, want only the new generated password, the one recorded", stdout)
	}
	if after.Gate.Key == before.Gate.Key || !after.Gate.HasPassword(password) {
		t.Errorf("gate = %+v, want a new key and the new password's hash", after.Gate)
	}
	if got := lastPublished(t, project); got.Key != after.Gate.Key {
		t.Error("the edge was not handed the rotated gate")
	}
	if strings.Contains(stderr, password) {
		t.Error("the run narration carries the password, which belongs on stdout alone")
	}
	if len(after.Gate.BypassSecrets) != 1 {
		t.Error("rotating the password dropped a bypass secret")
	}
}

func TestRotateUnderJSONIsOneResultCarryingThePasswordAndTheEdges(t *testing.T) {
	useJSONOutput(t)
	project := protectedProject(t)

	stdout, _, err := executePreview(t, project, "", "password", "rotate")

	if err != nil {
		t.Fatal(err)
	}
	var rotated resultv1.PasswordResult
	clitest.DecodeResultInto(t, stdout, &rotated)
	if rotated.GetSlug() != clitest.FixtureSlug || rotated.GetPassword() == "" || rotated.GetPassword() != recorded(t, project).Password {
		t.Errorf("result = %v, want the slug and the new password", &rotated)
	}
	if len(rotated.GetEdges()) != 1 || !rotated.GetEdges()[0].GetPublished() {
		t.Errorf("edges = %v, want the relay edge, published", rotated.GetEdges())
	}
}

func TestAPublicationThatFailsStillPrintsTheNewPasswordAndExitsNonZeroNamingSync(t *testing.T) {
	project := protectedProject(t)
	relayEdge(project).RefusePublication(assertedFailure("the edge is unreachable"))

	stdout, _, err := executePreview(t, project, "", "password", "rotate")

	if strings.TrimSpace(stdout) != recorded(t, project).Password || strings.TrimSpace(stdout) == "" {
		t.Errorf("stdout = %q, want the new password: it is recorded and the next `password` shows it, but the caller may be a pipeline", stdout)
	}
	if err == nil || !strings.Contains(err.Error(), "ocel protection sync --preview") || !strings.Contains(err.Error(), "the edge is unreachable") {
		t.Errorf("err = %v, want it to name `ocel protection sync --preview`, which publishes the preview tier's credentials, and the edge's reason", err)
	}
}

func TestSetTakesThePasswordFromStandardInputAndPrintsNothing(t *testing.T) {
	project := protectedProject(t)

	stdout, stderr, err := executePreview(t, project, "correct horse battery\n", "password", "set")

	if err != nil {
		t.Fatalf("set: %v; stderr=%s", err, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	record := recorded(t, project)
	if record.Password != "correct horse battery" || !record.Gate.HasPassword("correct horse battery") {
		t.Errorf("record = %+v, want the piped password without its newline", record)
	}
	if got := lastPublished(t, project); got.PasswordHash != record.Gate.PasswordHash {
		t.Error("the edge was not handed the new password's hash")
	}
}

func TestSetRefusesAnEmptyShortOrOversizedPasswordWithoutTouchingTheGate(t *testing.T) {
	for name, stdin := range map[string]string{
		"nothing":             "",
		"a newline":           "\n",
		"eleven characters":   "elevenchars",
		"over 256 bytes":      strings.Repeat("a", 257),
		"short but multibyte": "ééééééééééé",
	} {
		t.Run(name, func(t *testing.T) {
			project := protectedProject(t)
			if _, _, err := executePreview(t, project, "", "bypass", "create"); err != nil {
				t.Fatal(err)
			}
			before := recorded(t, project)

			_, _, err := executePreview(t, project, stdin, "password", "set")

			if err == nil {
				t.Fatalf("set with %q: err = nil, want a refusal", name)
			}
			if after := recorded(t, project); after.Gate.Key != before.Gate.Key {
				t.Error("a refused password rotated the key")
			}
		})
	}
}

func TestSetAcceptsTwelveMultibyteCharactersAndTwoHundredFiftySixBytes(t *testing.T) {
	for name, stdin := range map[string]string{
		"twelve characters of two bytes": strings.Repeat("é", 12),
		"256 bytes":                      strings.Repeat("a", 256),
	} {
		t.Run(name, func(t *testing.T) {
			project := protectedProject(t)

			if _, stderr, err := executePreview(t, project, stdin+"\n", "password", "set"); err != nil {
				t.Fatalf("set: %v; stderr=%s", err, stderr)
			}
		})
	}
}

func TestSetRefusesAPasswordTypedAtATerminalAndNamesTheWaysToSetOne(t *testing.T) {
	typingAtATerminal(t)
	project := protectedProject(t)

	_, _, err := executePreview(t, project, "", "password", "set")

	if err == nil || !strings.Contains(err.Error(), "standard input") || !strings.Contains(err.Error(), "ocel protection password rotate") {
		t.Errorf("set at a terminal: err = %v, want it to say the password is read from standard input and offer rotate", err)
	}
}
