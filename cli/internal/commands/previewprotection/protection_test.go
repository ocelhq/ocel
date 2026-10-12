package previewprotection

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
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func protectedProject(t *testing.T) clitest.FakeProject {
	t.Helper()
	return projectWith(t, `previews: { protection: true },`)
}

func unprotectedProject(t *testing.T) clitest.FakeProject {
	t.Helper()
	return projectWith(t, "")
}

func projectWith(t *testing.T, previews string) clitest.FakeProject {
	t.Helper()
	project := clitest.SetUpProject(t)
	clitest.Bootstrap(t, project.Provider, environment.TierPreview, fake.FeatureCache, fake.FeatureImages)
	clitest.RecordEdgeStack(t, project, environment.TierPreview, fake.KindRelay)
	clitest.WriteFile(t, filepath.Join(project.Root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
  `+previews+`
};
`)
	return project
}

func relayEdge(project clitest.FakeProject) *fake.Edge {
	return project.Provider.Edges().(*fake.Edges).Edge(fake.KindRelay)
}

func recorded(t *testing.T, project clitest.FakeProject) stackrecords.PreviewGateRecord {
	t.Helper()
	records, err := stackrecords.ReadPreviewGates(context.Background(), project.Provider.KeyValues(), project.Provider.Cipher())
	if err != nil {
		t.Fatal(err)
	}
	return records[clitest.FixtureSlug]
}

func lastPublished(t *testing.T, project clitest.FakeProject) previewgate.Gate {
	t.Helper()
	published := relayEdge(project).Publications()
	if len(published) == 0 {
		t.Fatal("no edge was published a gate")
	}
	return *published[len(published)-1].Gate
}

func execute(t *testing.T, project clitest.FakeProject, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Chdir(project.Root)
	invocation := newTestInvocation()
	var out, run bytes.Buffer
	clitest.AttachTerminalSink(invocation, &run)
	parent := &cobra.Command{Use: "preview", SilenceUsage: true, SilenceErrors: true}
	parent.AddCommand(NewCommand(invocation))
	parent.SetOut(&out)
	parent.SetErr(&run)
	parent.SetIn(strings.NewReader(stdin))
	parent.SetArgs(append([]string{"protection"}, args...))
	err = parent.ExecuteContext(context.Background())
	return out.String(), run.String(), err
}

func TestCredentialCommandsRefuseWhileTheConfigDoesNotProtectPreviewsAndPointAtTheSwitch(t *testing.T) {
	project := unprotectedProject(t)
	for _, args := range [][]string{
		{"password"}, {"password", "rotate"}, {"password", "set"}, {"bypass", "create"}, {"bypass", "ls"},
		{"bypass", "revoke", "0123456789ab"}, {"sync"},
	} {
		stdout, _, err := execute(t, project, "a long enough password", args...)

		if err == nil || !strings.Contains(err.Error(), "previews.protection") {
			t.Errorf("%v: err = %v, want a refusal that points at previews.protection", args, err)
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
	for _, want := range []string{"test-app", "not protected", "previews.protection"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to mention %q", stdout, want)
		}
	}
}

func TestTheStatusOfAProtectedProjectNamesThePasswordTheBypassSecretsAndTheEdges(t *testing.T) {
	project := protectedProject(t)
	if _, _, err := execute(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}
	record := recorded(t, project)

	stdout, _, err := execute(t, project, "")

	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"protected", "password", "1 bypass secret", string(fake.KindRelay), "live"} {
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
	_, _, _ = execute(t, project, "", "bypass", "create")

	stdout, _, err := execute(t, project, "")

	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pending", "ocel preview protection sync"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to mention %q", stdout, want)
		}
	}
}

func TestTheStatusAsJSONIsOneResultWithTheSwitchTheBypassIDsAndEachEdge(t *testing.T) {
	useJSONOutput(t)
	project := protectedProject(t)
	if _, _, err := execute(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := execute(t, project, "")

	if err != nil {
		t.Fatal(err)
	}
	var status resultv1.PreviewProtectionResult
	clitest.DecodeResultInto(t, stdout, &status)
	if status.GetSlug() != clitest.FixtureSlug || !status.GetEnabled() || !status.GetRecorded() || len(status.GetBypassSecretIds()) != 1 {
		t.Errorf("result = %v, want the slug, the switch on, a recorded gate and one bypass id", &status)
	}
	if edges := status.GetEdges(); len(edges) != 1 || edges[0].GetKind() != string(fake.KindRelay) || !edges[0].GetPublished() {
		t.Errorf("edges = %v, want the relay edge, published", edges)
	}
}

func TestSyncPublishesTheRecordedGateToTheEdgeAndCarriesNothingNew(t *testing.T) {
	project := protectedProject(t)
	if _, _, err := execute(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}
	before := recorded(t, project)

	stdout, stderr, err := execute(t, project, "", "sync")

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
	created, _, err := execute(t, project, "", "bypass", "create")
	if err != nil {
		t.Fatal(err)
	}
	var secret resultv1.PreviewBypassSecretResult
	clitest.DecodeResultInto(t, created, &secret)

	for name, args := range map[string][]string{
		"set":    {"password", "set"},
		"revoke": {"bypass", "revoke", secret.GetId()},
		"sync":   {"sync"},
	} {
		stdout, _, err := execute(t, project, "a password of enough length", args...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var published resultv1.PreviewPublicationResult
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
	if _, _, err := execute(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}
	relayEdge(project).RefusePublication(assertedFailure("the edge is unreachable"))

	for name, args := range map[string][]string{
		"rotate": {"password", "rotate"},
		"create": {"bypass", "create"},
		"set":    {"password", "set"},
		"sync":   {"sync"},
	} {
		stdout, _, err := execute(t, project, "a password of enough length", args...)

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
	if _, _, err := execute(t, project, "", "bypass", "create"); err != nil {
		t.Fatal(err)
	}
	err := stackrecords.ChangePreviewGates(context.Background(), project.Provider.KeyValues(), project.Provider.Cipher(), func(records map[string]stackrecords.PreviewGateRecord) error {
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
		stdout, stderr, err := execute(t, project, "", args...)

		if err == nil || !strings.Contains(err.Error()+stderr, "opened its previews") {
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
