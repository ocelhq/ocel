package protection

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
)

func TestBypassSecretsAreCreatedListedByIDAndRevoked(t *testing.T) {
	project := protectedProject(t)

	created, stderr, err := executePreview(t, project, "", "bypass", "create")
	if err != nil {
		t.Fatalf("create: %v; stderr=%s", err, stderr)
	}
	secret := strings.TrimSpace(created)
	if gate := recorded(t, project).Gate; secret == "" || !slices.Contains(gate.BypassSecrets, secret) {
		t.Fatalf("the recorded gate = %+v, want the created bypass secret", gate)
	}
	if strings.Contains(stderr, secret) {
		t.Error("the run narration carries the secret, which belongs on stdout alone")
	}

	listed, _, err := executePreview(t, project, "", "bypass", "ls")
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(listed)
	if len(id) != 12 || strings.Contains(listed, secret) {
		t.Fatalf("ls stdout = %q, want one 12 character id and never a secret", listed)
	}

	if _, stderr, err := executePreview(t, project, "", "bypass", "revoke", id); err != nil {
		t.Fatalf("revoke: %v; stderr=%s", err, stderr)
	}
	if gate := recorded(t, project).Gate; len(gate.BypassSecrets) != 0 || gate.PasswordMAC == "" {
		t.Errorf("the recorded gate = %+v, want the password kept and no bypass secret", gate)
	}
}

func TestRevokeNeverAsksForConfirmationBecauseTheProjectStaysProtected(t *testing.T) {
	project := protectedProject(t)
	if _, _, err := executePreview(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}
	listed, _, _ := executePreview(t, project, "", "bypass", "ls")

	_, _, err := executePreview(t, project, "", "bypass", "revoke", strings.TrimSpace(listed), "--yes")

	if err == nil || !strings.Contains(err.Error(), "yes") {
		t.Errorf("revoke --yes: err = %v, want the flag unknown: nothing is asked any more", err)
	}
}

func TestRevokingABypassSecretTheProjectDoesNotHaveNamesTheID(t *testing.T) {
	project := protectedProject(t)
	if _, _, err := executePreview(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}

	_, _, err := executePreview(t, project, "", "bypass", "revoke", "000000000000")

	if err == nil || !strings.Contains(err.Error(), "000000000000") {
		t.Fatalf("revoke: err = %v, want a refusal naming the id", err)
	}
}

func TestRevokeRotatesTheKeySoTheSessionsTheSecretOpenedEnd(t *testing.T) {
	project := protectedProject(t)
	if _, _, err := executePreview(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}
	listed, _, _ := executePreview(t, project, "", "bypass", "ls")
	before := recorded(t, project)

	if _, _, err := executePreview(t, project, "", "bypass", "revoke", strings.TrimSpace(listed)); err != nil {
		t.Fatal(err)
	}

	if after := recorded(t, project); after.Gate.Key == before.Gate.Key {
		t.Error("revoking a bypass secret kept the key, and every session it opened stays valid")
	}
}

func TestCreatingABypassSecretKeepsTheKeySoSessionsSurvive(t *testing.T) {
	project := protectedProject(t)
	if _, _, err := executePreview(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}
	before := recorded(t, project)

	if _, _, err := executePreview(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}

	if after := recorded(t, project); after.Gate.Key != before.Gate.Key || len(after.Gate.BypassSecrets) != 2 {
		t.Errorf("gate = %+v, want the same key and a second secret", after.Gate)
	}
}

func TestUnderJSONCreateAndLsPrintOneResultEach(t *testing.T) {
	useJSONOutput(t)
	project := protectedProject(t)

	stdout, _, err := executePreview(t, project, "", "bypass", "create")
	if err != nil {
		t.Fatal(err)
	}
	var created resultv1.BypassSecretResult
	clitest.DecodeResultInto(t, stdout, &created)
	if created.GetSlug() != clitest.FixtureSlug || created.GetSecret() == "" || len(created.GetId()) != 12 {
		t.Errorf("result = %v, want a bypass secret and its id", &created)
	}

	stdout, _, err = executePreview(t, project, "", "bypass", "ls")
	if err != nil {
		t.Fatal(err)
	}
	var listed resultv1.BypassSecretListResult
	clitest.DecodeResultInto(t, stdout, &listed)
	if listed.GetSlug() != clitest.FixtureSlug || !slices.Equal(listed.GetIds(), []string{created.GetId()}) {
		t.Errorf("result = %v, want the slug and the one bypass secret's id", &listed)
	}
	if strings.Contains(stdout, "edges") || strings.Contains(stdout, "enabled") {
		t.Errorf("ls under JSON printed %s, want the ids alone and not the whole status", stdout)
	}
	if strings.Contains(stdout, created.GetSecret()) {
		t.Error("ls under JSON revealed a secret")
	}
}

func TestListingBypassSecretsOfAProjectWithNoneSaysSoAndHowToMakeOne(t *testing.T) {
	project := protectedProject(t)

	stdout, _, err := executePreview(t, project, "", "bypass", "ls")

	if err != nil || !strings.Contains(stdout, "no preview bypass secrets") || !strings.Contains(stdout, "ocel protection bypass create --preview") {
		t.Errorf("ls = %q, %v, want it to say there are none and name `bypass create`", stdout, err)
	}
}

func TestAProjectHoldsAtMostEightBypassSecretsAndTheNinthIsRefusedNamingRevoke(t *testing.T) {
	project := protectedProject(t)
	for range 8 {
		if _, _, err := executePreview(t, project, "", "bypass", "create"); err != nil {
			t.Fatal(err)
		}
	}

	stdout, _, err := executePreview(t, project, "", "bypass", "create")

	if err == nil || !strings.Contains(err.Error(), "ocel protection bypass revoke") {
		t.Errorf("ninth create: err = %v, want a refusal naming `bypass revoke`", err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
}
