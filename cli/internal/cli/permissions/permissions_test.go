package permissions

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/runui"
	"github.com/ocelhq/ocel/pkg/edge"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestPermissionsNeedsATier(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no purpose", nil},
		{"a purpose that is neither", []string{"admin"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd := NewCommand(cmddeps.Deps{})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(tc.args)
			err := cmd.Execute()
			if err == nil {
				t.Fatal("Execute err = nil, want permissions without a credential purpose to be a failure")
			}
			for _, want := range []string{"bootstrap", "deploy"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to name the %s purpose", err, want)
				}
			}
			if !strings.Contains(out.String(), "permissions <bootstrap|deploy>") {
				t.Errorf("output = %q, want the permissions help", out.String())
			}
		})
	}
}

func TestPermissionsPurposeArg(t *testing.T) {
	t.Parallel()

	for typed, want := range map[string]contractv1.CredentialPurpose{
		"deploy":    contractv1.CredentialPurpose_CREDENTIAL_PURPOSE_DEPLOY,
		"bootstrap": contractv1.CredentialPurpose_CREDENTIAL_PURPOSE_BOOTSTRAP,
	} {
		got, err := purposeArg([]string{typed})
		if err != nil {
			t.Fatalf("purposeArg(%q) err = %v", typed, err)
		}
		if got != want {
			t.Errorf("purposeArg(%q) = %v, want %v", typed, got, want)
		}
	}
	if _, err := purposeArg([]string{"admin"}); err == nil || !strings.Contains(err.Error(), `"admin"`) {
		t.Errorf("purposeArg err = %v, want it to name what was typed", err)
	}
}

func TestRunPermissions(t *testing.T) {
	t.Run("it writes the document the provider renders for the purpose", func(t *testing.T) {
		project := clitest.SetUpProject(t)
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stderr)
		if err := Run(context.Background(), deps, project.Root, contractv1.CredentialPurpose_CREDENTIAL_PURPOSE_DEPLOY, &stdout); err != nil {
			t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "fake permissions for deploy") {
			t.Errorf("stdout = %q, want the deploy purpose's document", stdout.String())
		}
		if strings.Contains(stdout.String(), "fake credentials") {
			t.Errorf("stdout = %q, want a lone group to print pipeable, without its heading", stdout.String())
		}
	})

	t.Run("it writes the bootstrap document when the bootstrap purpose is asked for", func(t *testing.T) {
		project := clitest.SetUpProject(t)
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stderr)
		if err := Run(context.Background(), deps, project.Root, contractv1.CredentialPurpose_CREDENTIAL_PURPOSE_BOOTSTRAP, &stdout); err != nil {
			t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "fake permissions for bootstrap") {
			t.Errorf("stdout = %q, want the bootstrap purpose's document", stdout.String())
		}
	})

	t.Run("it heads each group where the edge has credentials of its own", func(t *testing.T) {
		project := clitest.SetUpProject(t)
		project.Provider.Edges().(*fake.Edges).Edge(fake.KindRelay).DocumentsPermissions(edge.CredentialDocument{
			Heading:  "relay token",
			Document: "Account · Relay Scripts · Edit",
		})
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(deps, &stderr)
		if err := Run(context.Background(), deps, project.Root, contractv1.CredentialPurpose_CREDENTIAL_PURPOSE_DEPLOY, &stdout); err != nil {
			t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		for _, want := range []string{
			"fake credentials",
			"fake permissions for deploy",
			"relay token",
			"Account · Relay Scripts · Edit for deploy",
		} {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("stdout = %q, want it to include %q", stdout.String(), want)
			}
		}
	})
}

func TestPermissionsStartsTheProviderInTheCheckPhaseOfItsRunAndPrintsTheDocumentAloneOnStdout(t *testing.T) {
	root := clitest.SetUpProject(t).Root
	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	deps.Presentation = func(io.Writer) runui.Presentation {
		return runui.Resolve(runui.Origin{LogFormat: runui.FormatJSON})
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stderr)
	if err := Run(context.Background(), deps, root, contractv1.CredentialPurpose_CREDENTIAL_PURPOSE_DEPLOY, &stdout); err != nil {
		t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	var phases []progressv1.Phase
	var result *streamv1.RunSummary
	for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		ev := &streamv1.RunEvent{}
		if err := protojson.Unmarshal([]byte(line), ev); err != nil {
			t.Fatalf("stream line %q is not a protojson RunEvent: %v", line, err)
		}
		if ev.GetStarted() != nil && len(ev.GetStarted().GetParentSpanId()) == 0 {
			phases = append(phases, ev.GetPhase())
		}
		if ev.GetSummary() != nil {
			result = ev.GetSummary()
		}
	}
	if len(phases) == 0 || phases[0] != progressv1.Phase_PHASE_CHECK {
		t.Errorf("phases = %v, want the run to open with the check phase that starts the provider", phases)
	}
	if !result.GetSuccess() {
		t.Errorf("result = %v, want the run to succeed", result)
	}
	if strings.TrimSpace(stdout.String()) == "" || strings.Contains(stderr.String(), "fake permissions for deploy") {
		t.Errorf("stdout = %q, stream = %q: want the document on stdout and not on the stream", stdout.String(), stderr.String())
	}
}
