package protection

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func protectedProject(t *testing.T) clitest.FakeProject {
	t.Helper()
	return projectWith(t, `protection: { production: true, preview: true },`)
}

func unprotectedProject(t *testing.T) clitest.FakeProject {
	t.Helper()
	return projectWith(t, "")
}

func projectWith(t *testing.T, protection string) clitest.FakeProject {
	t.Helper()
	project := clitest.SetUpProject(t)
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		clitest.Bootstrap(t, project.Provider, tier, fake.PartCache, fake.PartImages)
		clitest.RecordEdgeStack(t, project, tier, fake.KindRelay)
	}
	clitest.WriteFile(t, filepath.Join(project.Root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  provider: { fake: {} },
  domains: { production: "acme.com", preview: "*.preview.acme.com" },
  `+protection+`
};
`)
	return project
}

func relayEdge(project clitest.FakeProject) *fake.Edge {
	return project.Provider.Edges().(*fake.Edges).Edge(fake.KindRelay)
}

func recorded(t *testing.T, project clitest.FakeProject) stackrecords.GateRecord {
	t.Helper()
	return recordedIn(t, project, environment.TierPreview)
}

func recordedIn(t *testing.T, project clitest.FakeProject, tier environment.Tier) stackrecords.GateRecord {
	t.Helper()
	records, err := stackrecords.ReadGates(context.Background(), project.Provider.KeyValues(), project.Provider.Cipher(), tier)
	if err != nil {
		t.Fatal(err)
	}
	return records[clitest.FixtureSlug]
}

func lastPublished(t *testing.T, project clitest.FakeProject) gate.Gate {
	t.Helper()
	published := relayEdge(project).Publications()
	if len(published) == 0 {
		t.Fatal("no edge was published a gate")
	}
	return *published[len(published)-1].Gate
}

func executePreview(t *testing.T, project clitest.FakeProject, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return execute(t, project, stdin, append(args, "--preview")...)
}

func execute(t *testing.T, project clitest.FakeProject, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Chdir(project.Root)
	invocation := newTestInvocation()
	var out, run bytes.Buffer
	clitest.AttachTerminalSink(invocation, &run)
	parent := &cobra.Command{Use: "ocel", SilenceUsage: true, SilenceErrors: true}
	parent.AddCommand(NewCommand(invocation))
	parent.SetOut(&out)
	parent.SetErr(&run)
	parent.SetIn(strings.NewReader(stdin))
	parent.SetArgs(append([]string{"protection"}, args...))
	err = parent.ExecuteContext(context.Background())
	return out.String(), run.String(), err
}

func TestCredentialCommandsRefuseWhileTheConfigDoesNotProtectAndPointAtTheSwitch(t *testing.T) {
	project := unprotectedProject(t)
	for _, args := range [][]string{
		{"password"}, {"password", "rotate"}, {"password", "set"}, {"bypass", "create"}, {"bypass", "ls"},
		{"bypass", "revoke", "0123456789ab"}, {"sync"},
	} {
		stdout, _, err := executePreview(t, project, "a long enough password", args...)

		if err == nil || !strings.Contains(err.Error(), "protection.preview") {
			t.Errorf("%v: err = %v, want a refusal that points at protection.preview", args, err)
		}
		if stdout != "" {
			t.Errorf("%v: stdout = %q, want nothing", args, stdout)
		}
	}
	if len(relayEdge(project).Publications()) != 0 {
		t.Error("a refused command published a gate")
	}
}

func TestTheStatusOfAnUnprotectedProjectSaysWhyAndWhereToTurnItOn(t *testing.T) {
	project := unprotectedProject(t)

	stdout, _, err := execute(t, project, "")

	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"test-app", "not protected", "protection.preview"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to mention %q", stdout, want)
		}
	}
}

func TestTheStatusOfAProtectedProjectNamesThePasswordTheBypassSecretsAndTheEdges(t *testing.T) {
	project := protectedProject(t)
	if _, _, err := executePreview(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}
	record := recorded(t, project)

	stdout, _, err := execute(t, project, "")

	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"protected", "password", "bypass secrets  1 (", string(fake.KindRelay), "live"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to mention %q", stdout, want)
		}
	}
	if strings.Contains(stdout, record.Password) || strings.Contains(stdout, record.Gate.BypassSecrets[0]) {
		t.Errorf("stdout = %q, want no secret: the status only says what exists", stdout)
	}
}

func TestTheStatusOfAProjectWhoseLastPublicationFailedSaysPendingAndNamesSync(t *testing.T) {
	project := protectedProject(t)
	relayEdge(project).RefusePublication(assertedFailure("the edge is unreachable"))
	_, _, _ = executePreview(t, project, "", "bypass", "create")

	stdout, _, err := execute(t, project, "")

	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pending", "ocel protection sync"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to mention %q", stdout, want)
		}
	}
}

func TestTheStatusAsJSONIsOneResultWithBothTiersTheirSwitchesTheBypassIDsAndEachEdge(t *testing.T) {
	useJSONOutput(t)
	project := protectedProject(t)
	if _, _, err := executePreview(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := execute(t, project, "")

	if err != nil {
		t.Fatal(err)
	}
	var status resultv1.ProtectionResult
	clitest.DecodeResultInto(t, stdout, &status)
	if status.GetSlug() != clitest.FixtureSlug || len(status.GetTiers()) != 2 {
		t.Fatalf("result = %v, want the slug and one entry per tier", &status)
	}
	production, preview := status.GetTiers()[0], status.GetTiers()[1]
	if production.GetTier() != environmentv1.Tier_TIER_PRODUCTION || !production.GetEnabled() || production.GetRecorded() || len(production.GetBypassSecretIds()) != 0 {
		t.Errorf("production = %v, want the switch on and nothing recorded yet", production)
	}
	if preview.GetTier() != environmentv1.Tier_TIER_PREVIEW || !preview.GetEnabled() || !preview.GetRecorded() || len(preview.GetBypassSecretIds()) != 1 {
		t.Errorf("preview = %v, want the switch on, a recorded gate and one bypass id", preview)
	}
	if edges := preview.GetEdges(); len(edges) != 1 || edges[0].GetKind() != string(fake.KindRelay) || !edges[0].GetPublished() {
		t.Errorf("edges = %v, want the relay edge, published", edges)
	}
}

func TestSyncPublishesTheRecordedGateToTheEdgeAndCarriesNothingNew(t *testing.T) {
	project := protectedProject(t)
	if _, _, err := executePreview(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}
	before := recorded(t, project)

	stdout, stderr, err := executePreview(t, project, "", "sync")

	if err != nil {
		t.Fatalf("sync: %v; stderr=%s", err, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing: a sync reveals no secret", stdout)
	}
	if published := lastPublished(t, project); published.Key != before.Gate.Key {
		t.Errorf("published key %q, want the recorded one", published.Key)
	}
	if after := recorded(t, project); after.Gate.Key != before.Gate.Key || after.Password != before.Password {
		t.Error("a sync changed the recorded gate")
	}
}

type assertedFailure string

func (e assertedFailure) Error() string { return string(e) }

func TestUnderJSONSetRevokeAndSyncPrintWhichEdgesTookTheChangeAndNoSecret(t *testing.T) {
	useJSONOutput(t)
	project := protectedProject(t)
	created, _, err := executePreview(t, project, "", "bypass", "create")
	if err != nil {
		t.Fatal(err)
	}
	var secret resultv1.BypassSecretResult
	clitest.DecodeResultInto(t, created, &secret)

	for name, args := range map[string][]string{
		"set":    {"password", "set"},
		"revoke": {"bypass", "revoke", secret.GetId()},
		"sync":   {"sync"},
	} {
		stdout, _, err := executePreview(t, project, "a password of enough length", args...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var published resultv1.PublicationResult
		clitest.DecodeResultInto(t, stdout, &published)
		if published.GetSlug() != clitest.FixtureSlug || len(published.GetEdges()) != 1 || !published.GetEdges()[0].GetPublished() {
			t.Errorf("%s: result = %v, want the relay edge, published", name, &published)
		}
		if strings.Contains(stdout, "a password of enough length") || strings.Contains(stdout, secret.GetSecret()) {
			t.Errorf("%s: stdout = %s, want no secret", name, stdout)
		}
	}
}

func TestUnderJSONAFailedPublicationPrintsOneResultAndLeavesTheRootNothingMoreToPrint(t *testing.T) {
	useJSONOutput(t)
	project := protectedProject(t)
	if _, _, err := executePreview(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}
	relayEdge(project).RefusePublication(assertedFailure("the edge is unreachable"))

	for name, args := range map[string][]string{
		"rotate": {"password", "rotate"},
		"create": {"bypass", "create"},
		"set":    {"password", "set"},
		"sync":   {"sync"},
	} {
		stdout, _, err := executePreview(t, project, "a password of enough length", args...)

		if lines := strings.Split(strings.TrimSpace(stdout), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], `{"ok":true`) {
			t.Errorf("%s: stdout = %q, want the one result document", name, stdout)
		}
		var exit *exitcode.ExitError
		if !errors.As(err, &exit) || exit.Code == 0 || exit.Err != nil {
			t.Errorf("%s: err = %#v, want a bare non-zero exit code: the run summary reported the failure and the result is on stdout, so a cause would make the root print a second document", name, err)
		}
	}
}

func TestCredentialCommandsRefuseWhileTheLastDeployOpenedThePreviewsAndNameTheDeployThatRestoresThem(t *testing.T) {
	project := protectedProject(t)
	if _, _, err := executePreview(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}
	err := stackrecords.ChangeGates(context.Background(), project.Provider.KeyValues(), project.Provider.Cipher(), environment.TierPreview, func(records map[string]stackrecords.GateRecord) error {
		record := records[clitest.FixtureSlug]
		record.Enabled = false
		records[clitest.FixtureSlug] = record
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	published := len(relayEdge(project).Publications())

	for _, args := range [][]string{{"password"}, {"bypass", "ls"}, {"sync"}} {
		stdout, stderr, err := executePreview(t, project, "", args...)

		if err == nil || !strings.Contains(err.Error()+stderr, "opened the previews") {
			t.Errorf("%v: err = %v, stderr = %s, want a refusal saying the last deploy opened the previews", args, err, stderr)
		}
		if stdout != "" {
			t.Errorf("%v: stdout = %q, want nothing: the kept credentials protect nothing until a preview deploy restores them", args, stdout)
		}
	}
	if got := len(relayEdge(project).Publications()); got != published {
		t.Error("a refused command published a gate")
	}
}
