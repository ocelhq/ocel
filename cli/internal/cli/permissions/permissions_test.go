package permissions

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestPermissionsNeedsATier(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no tier", nil},
		{"a tier that is neither", []string{"admin"}},
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
				t.Fatal("Execute err = nil, want permissions without a credential tier to be a failure")
			}
			for _, want := range []string{"bootstrap", "deploy"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to name the %s tier", err, want)
				}
			}
			if !strings.Contains(out.String(), "permissions <bootstrap|deploy>") {
				t.Errorf("output = %q, want the permissions help", out.String())
			}
		})
	}
}

func TestPermissionsTierArg(t *testing.T) {
	t.Parallel()

	got, err := tierArg([]string{"deploy"})
	if err != nil {
		t.Fatalf("tierArg err = %v", err)
	}
	if got != contractv1.CredentialTier_CREDENTIAL_TIER_DEPLOY {
		t.Errorf("tierArg = %v, want the deploy tier", got)
	}
	if _, err := tierArg([]string{"admin"}); err == nil || !strings.Contains(err.Error(), `"admin"`) {
		t.Errorf("tierArg err = %v, want it to name what was typed", err)
	}
}

func TestRunPermissions(t *testing.T) {
	t.Run("it writes the document the provider renders for the tier", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := clitest.NewDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)

		var stdout, stderr bytes.Buffer
		if err := Run(context.Background(), deps, root, contractv1.CredentialTier_CREDENTIAL_TIER_DEPLOY, &stdout, &stderr); err != nil {
			t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "CREDENTIAL_TIER_DEPLOY") {
			t.Errorf("stdout = %q, want the deploy tier's document", stdout.String())
		}
		if strings.Contains(stdout.String(), "AWS credentials") {
			t.Errorf("stdout = %q, want a lone group to print pipeable, without its heading", stdout.String())
		}
	})

	t.Run("it heads each group where the edge carries credentials of its own", func(t *testing.T) {
		root, _, deps := clitest.SetUpEdgeFixture(t, "  edge: { kind: \"cloudflare\", options: {} },\n")

		var stdout, stderr bytes.Buffer
		if err := Run(context.Background(), deps, root, contractv1.CredentialTier_CREDENTIAL_TIER_DEPLOY, &stdout, &stderr); err != nil {
			t.Fatalf("Run err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		for _, want := range []string{
			"AWS credentials",
			"CREDENTIAL_TIER_DEPLOY",
			"Cloudflare API token",
			"Account · Workers Scripts · Edit",
		} {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("stdout = %q, want it to carry %q", stdout.String(), want)
			}
		}
	})
}
