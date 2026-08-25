package bootstrap

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func runCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()

	cmd := NewCommand(cmddeps.Deps{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestBootstrapNeedsASubcommand(t *testing.T) {
	t.Parallel()

	t.Run("bare, it prints its help and fails", func(t *testing.T) {
		t.Parallel()

		out, err := runCommand(t)
		if err == nil {
			t.Fatal("Execute err = nil, want bootstrap without a subcommand to be a failure")
		}
		for _, want := range []string{"production", "preview", "destroy", "policy", "status"} {
			if !strings.Contains(out, want) {
				t.Errorf("output = %q, want the help to list %q", out, want)
			}
		}
	})

	t.Run("a subcommand that is not one fails", func(t *testing.T) {
		t.Parallel()

		if _, err := runCommand(t, "staging"); err == nil {
			t.Fatal("Execute err = nil, want a subcommand that is not one to be a failure")
		}
	})
}

func TestBootstrapClassCommands(t *testing.T) {
	t.Parallel()

	cmd := NewCommand(cmddeps.Deps{})
	for _, tc := range []struct {
		typed string
		want  string
	}{
		{"production", "production"},
		{"prod", "production"},
		{"preview", "preview"},
	} {
		found, _, err := cmd.Find([]string{tc.typed})
		if err != nil {
			t.Fatalf("Find(%q) err = %v", tc.typed, err)
		}
		if found.Name() != tc.want {
			t.Errorf("Find(%q) = %q, want %q", tc.typed, found.Name(), tc.want)
		}
		for _, flag := range []string{"yes", "dry", "features", "force", "auto-heal"} {
			if found.Flags().Lookup(flag) == nil {
				t.Errorf("%s carries no --%s", found.Name(), flag)
			}
		}
	}

	for _, gone := range []string{"preview", "destroy", "print-policy"} {
		if cmd.Flags().Lookup(gone) != nil {
			t.Errorf("bootstrap still carries --%s", gone)
		}
	}
}

func TestBootstrapDestroyNeedsAClass(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no class", []string{"destroy"}},
		{"a class that is not one", []string{"destroy", "staging"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, err := runCommand(t, tc.args...)
			if err == nil {
				t.Fatal("Execute err = nil, want a destroy without a class to be a failure")
			}
			if !strings.Contains(err.Error(), "preview") || !strings.Contains(err.Error(), "production") {
				t.Errorf("err = %v, want it to name both classes", err)
			}
			if !strings.Contains(out, "destroy <production|preview>") {
				t.Errorf("output = %q, want the destroy help", out)
			}
		})
	}
}

func TestBootstrapDestroyClassArgument(t *testing.T) {
	t.Parallel()

	for typed, want := range map[string]environmentv1.Tier{
		"preview":    environmentv1.Tier_TIER_PREVIEW,
		"production": environmentv1.Tier_TIER_PRODUCTION,
		"prod":       environmentv1.Tier_TIER_PRODUCTION,
	} {
		got, err := environmentArg([]string{typed})
		if err != nil {
			t.Fatalf("environmentArg(%q) err = %v", typed, err)
		}
		if got != want {
			t.Errorf("environmentArg(%q) = %v, want %v", typed, got, want)
		}
	}
}

func TestBootstrapPolicyNeedsATier(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no tier", []string{"policy"}},
		{"a tier that is neither", []string{"policy", "admin"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, err := runCommand(t, tc.args...)
			if err == nil {
				t.Fatal("Execute err = nil, want a policy without a credential tier to be a failure")
			}
			for _, want := range []string{"bootstrap", "deploy"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to name the %s tier", err, want)
				}
			}
			if !strings.Contains(out, "policy <bootstrap|deploy>") {
				t.Errorf("output = %q, want the policy help", out)
			}
		})
	}
}

func TestCredentialTier(t *testing.T) {
	t.Parallel()

	got, err := credentialTier("deploy")
	if err != nil {
		t.Fatalf("credentialTier err = %v", err)
	}
	if got != contractv1.CredentialTier_CREDENTIAL_TIER_DEPLOY {
		t.Errorf("credentialTier = %v, want the deploy tier", got)
	}
	if _, err := credentialTier("admin"); err == nil || !strings.Contains(err.Error(), `"admin"`) {
		t.Errorf("credentialTier err = %v, want it to name what was typed", err)
	}
}
