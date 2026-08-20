package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestConfirmPhrase(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		label  string
		phrase string
		input  string
		want   bool
		prompt string
	}{
		{"the exact project name proceeds", "project name", "proj_shop", "proj_shop\n", true, "Type the project name (proj_shop) to confirm:"},
		{"surrounding space is trimmed", "project name", "proj_shop", "  proj_shop  \n", true, "Type the project name (proj_shop) to confirm:"},
		{"a near miss aborts", "project name", "proj_shop", "proj_shopp\n", false, "Type the project name (proj_shop) to confirm:"},
		{"reflexive yes aborts", "project name", "proj_shop", "y\n", false, "Type the project name (proj_shop) to confirm:"},
		{"empty aborts", "project name", "proj_shop", "\n", false, "Type the project name (proj_shop) to confirm:"},
		{"closed stdin aborts", "project name", "proj_shop", "", false, "Type the project name (proj_shop) to confirm:"},
		{"the base domain is its own scope", "domain", "preview.acme.com", "preview.acme.com\n", true, "Type the domain (preview.acme.com) to confirm:"},
		{"another scope's phrase does not carry", "domain", "preview.acme.com", "proj_shop\n", false, "Type the domain (preview.acme.com) to confirm:"},
		{"the class name is the substrate's phrase", "class name", "preview", "preview\n", true, "Type the class name (preview) to confirm:"},
		{"the other class does not confirm this one", "class name", "preview", "production\n", false, "Type the class name (preview) to confirm:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			got, err := confirmPhrase(context.Background(), tc.label, tc.phrase, &stdout, strings.NewReader(tc.input))
			if err != nil {
				t.Fatalf("confirmPhrase() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("confirmPhrase(%q, %q) = %v, want %v", tc.phrase, tc.input, got, tc.want)
			}
			if !strings.Contains(stdout.String(), tc.prompt) {
				t.Errorf("stdout = %q, want the prompt %q", stdout.String(), tc.prompt)
			}
		})
	}
}

func TestPrintDestroyPlan(t *testing.T) {
	t.Parallel()

	t.Run("it lists every target", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		printDestroyPlan(&out, "proj_shop", false, &contractv1.PlanRemoveProjectResponse{
			EdgeStack: &contractv1.EdgeStackPlan{
				EdgeKind: "cloudflare",
				Items: []*contractv1.RemovalItem{
					{Kind: "edge stack", Name: "shop", Action: contractv1.RemovalItem_ACTION_DELETE},
					{Kind: "distribution", Name: "E1SHOP", Action: contractv1.RemovalItem_ACTION_DISABLE_THEN_DELETE, Slow: true},
					{Kind: "certificate", Name: "shop.example.com", Action: contractv1.RemovalItem_ACTION_KEEP, Reason: "you pinned this certificate"},
				},
			},
			InfraStacks: []string{"shop--infra"},
			AppStacks:   []string{"shop--web--b1", "shop--api--b2"},
		})
		got := out.String()
		for _, want := range []string{
			`production project "proj_shop"`,
			"fronted by the cloudflare edge",
			"delete edge stack shop",
			"disable, then delete distribution E1SHOP (this one is slow)",
			"infra stack shop--infra",
			"INCLUDING ALL DATA",
			"app stack shop--web--b1",
			"app stack shop--api--b2",
			"every production variable value",
			"This cannot be undone.",
			"Left in place:",
			"keep certificate shop.example.com — you pinned this certificate",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("printDestroyPlan output missing %q; got:\n%s", want, got)
			}
		}
		if strings.Index(got, "keep certificate") < strings.Index(got, "This cannot be undone.") {
			t.Errorf("printDestroyPlan listed a kept item among the doomed ones; got:\n%s", got)
		}
	})

	t.Run("with no edge bought the quota-paced deletions read as slow", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		printDestroyPlan(&out, "proj_shop", false, &contractv1.PlanRemoveProjectResponse{
			EdgeStack: &contractv1.EdgeStackPlan{
				EdgeKind: "api-gateway",
				Items: []*contractv1.RemovalItem{
					{Kind: "REST APIs", Name: "shop", Action: contractv1.RemovalItem_ACTION_DELETE, Slow: true},
					{Kind: "domain names", Name: "shop.example.com", Action: contractv1.RemovalItem_ACTION_DELETE},
				},
			},
		})
		got := out.String()
		if !strings.Contains(got, "delete REST APIs shop (this one is slow)") {
			t.Errorf("printDestroyPlan output missing the slow REST APIs line; got:\n%s", got)
		}
		if !strings.Contains(got, "delete domain names shop.example.com\n") {
			t.Errorf("printDestroyPlan marked an unpaced item slow; got:\n%s", got)
		}
	})

	t.Run("an action this CLI does not know reads as a sentence", func(t *testing.T) {
		t.Parallel()

		got := removalItemLine(&contractv1.RemovalItem{
			Kind:   "certificate",
			Name:   "shop.example.com",
			Action: contractv1.RemovalItem_Action(97),
		})
		if !strings.Contains(got, "an action this CLI does not know") || !strings.HasSuffix(got, "certificate shop.example.com") {
			t.Errorf("removalItemLine() = %q, want the unknown action named before the resource", got)
		}
	})
}

func TestCheckDestroyFlags(t *testing.T) {
	t.Parallel()

	t.Run("--yes is accepted only alongside --preview", func(t *testing.T) {
		t.Parallel()

		if err := checkDestroyFlags(true, true); err != nil {
			t.Errorf("--preview --yes rejected: %v", err)
		}
		if err := checkDestroyFlags(true, false); err != nil {
			t.Errorf("--preview rejected: %v", err)
		}
		if err := checkDestroyFlags(false, false); err != nil {
			t.Errorf("bare destroy rejected: %v", err)
		}
		err := checkDestroyFlags(false, true)
		if err == nil {
			t.Fatal("`ocel destroy --yes` accepted for production, want a refusal")
		}
		if !strings.Contains(err.Error(), "--preview") {
			t.Errorf("err = %v, want it to say --yes is only accepted with --preview", err)
		}
	})
}

func TestRunDestroyPreviewProject(t *testing.T) {
	t.Run("--yes skips the terminal check and the typed name", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDestroyPreviewProject(context.Background(), d, root, true, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyPreviewProject err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, want := range []string{
			`ENTIRE preview footprint of project "test-app"`,
			"fronted by the cloudfront edge",
			"delete edge workers test-app",
			"infra stack test-app--pr-1--infra — databases and buckets, INCLUDING ALL DATA",
			"infra stack test-app--pr-2--infra",
			"app stack test-app--pr-1--web--b1",
			"every preview variable value",
			"The account-level preview bootstrap is left intact. This cannot be undone.",
			"Left in place:",
			"keep preview wildcard *.preview.acme.com — substrate-scoped",
			"DESTROY PROJECT project=test-app dns= tier=TIER_PREVIEW",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		if strings.Index(out, "keep preview wildcard") < strings.Index(out, "This cannot be undone.") {
			t.Errorf("stdout listed a kept item among the doomed ones:\n%s", out)
		}
		if strings.Contains(out, "Type the project name") {
			t.Errorf("stdout = %q, want --yes to skip the typed-name confirmation", out)
		}
	})

	t.Run("the dns descriptor rides along so the teardown can delete what it wrote", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
  dns: { kind: "route53" },
};
`)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		t.Setenv(fakeInfraClassEnvVar, "preview")
		t.Setenv(fakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDestroyPreviewProject(context.Background(), d, root, true, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyPreviewProject err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if out := stdout.String(); !strings.Contains(out, "DESTROY PROJECT project=test-app dns=route53") {
			t.Errorf("stdout = %q, want the dns descriptor on the teardown request", out)
		}
	})

	t.Run("without --yes it refuses without a terminal", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := runDestroyPreviewProject(context.Background(), defaultDeps(), t.TempDir(), false, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDestroyPreviewProject without a TTY err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "interactive terminal") {
			t.Errorf("err = %v, want the no-TTY refusal", err)
		}
	})
}

func TestRunDestroy(t *testing.T) {
	t.Run("it refuses without a terminal", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := runDestroy(context.Background(), defaultDeps(), t.TempDir(), &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDestroy without a TTY err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "interactive terminal") {
			t.Errorf("err = %v, want the no-TTY refusal", err)
		}
	})

	t.Run("the project name gets past the terminal requirement and says so", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		t.Setenv(destroyBypassEnv, "test-app")

		var stdout, stderr bytes.Buffer
		err := runDestroy(context.Background(), d, root, &stdout, &stderr, strings.NewReader(""))
		if err != nil && strings.Contains(err.Error(), "interactive terminal") {
			t.Errorf("err = %v, want the bypass to get past the TTY requirement", err)
		}
		if strings.Contains(stdout.String(), "Type the project name") {
			t.Errorf("stdout = %q, want the bypass to skip the typed-name confirmation", stdout.String())
		}
		if !strings.Contains(stderr.String(), destroyBypassEnv) {
			t.Errorf("stderr = %q, want it to name %s so an unconfirmed destroy is never silent", stderr.String(), destroyBypassEnv)
		}
	})

	t.Run("it renders the plan the provider sent, kept items included", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		t.Setenv(fakeInfraClassEnvVar, "production")
		t.Setenv(fakeInfraPresentEnvVar, "1")
		t.Setenv(destroyBypassEnv, "test-app")

		var stdout, stderr bytes.Buffer
		if err := runDestroy(context.Background(), d, root, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, want := range []string{
			"fronted by the cloudfront edge",
			"delete edge stack test-app",
			"disable, then delete distribution E1test-app (this one is slow)",
			"keep certificate test-app.example.com — you pinned this certificate; Ocel never deletes one it did not request",
			"infra stack test-app--infra",
			"app stack test-app--web--b1",
			"DESTROY PROJECT project=test-app",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
	})

	t.Run("a value that is not this project's name is refused without a terminal", func(t *testing.T) {
		root, _ := setUpDeployFixture(t)
		d := defaultDeps()
		setLoggedIn(&d)
		stubAppFunctions(&d, nil)
		t.Setenv(destroyBypassEnv, "1")

		var stdout, stderr bytes.Buffer
		err := runDestroy(context.Background(), d, root, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDestroy err = nil, want an ambient %s=1 refused; stdout=%s", destroyBypassEnv, stdout.String())
		}
		if !strings.Contains(err.Error(), destroyBypassEnv) || !strings.Contains(err.Error(), "test-app") {
			t.Errorf("err = %v, want it to name %s and the project", err, destroyBypassEnv)
		}
	})

	t.Run("an unset bypass is not a bypass", func(t *testing.T) {
		t.Setenv(destroyBypassEnv, "")
		var stdout, stderr bytes.Buffer
		err := runDestroy(context.Background(), defaultDeps(), t.TempDir(), &stdout, &stderr, strings.NewReader(""))
		if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
			t.Errorf("err = %v, want the no-TTY refusal", err)
		}
	})
}
