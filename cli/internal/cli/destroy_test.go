package cli

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/changeplan"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func TestPrintDestroyPlan(t *testing.T) {
	t.Parallel()

	t.Run("it lists every target", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		printDestroyPlan(&out, "proj_shop", false, &contractv1.ChangePlan{
			EdgeKind: "cloudfront",
			Subject:  "proj_shop",
			Groups: []*contractv1.ChangeGroup{
				{
					Kind:    "stack",
					Name:    "aws/shop--infra",
					Feature: "infra",
					Action:  contractv1.Change_ACTION_DELETE,
					Reason:  "databases and buckets, INCLUDING ALL DATA",
				},
				{Kind: "stack", Name: "aws/shop--web--b1", Feature: "web", Action: contractv1.Change_ACTION_DELETE},
				{
					Kind:   "edge",
					Name:   "cloudfront/edge",
					Action: contractv1.Change_ACTION_DELETE,
					Changes: []*contractv1.Change{
						{
							Kind:   "AWS::CloudFront::Distribution",
							Name:   "E1SHOP",
							Action: contractv1.Change_ACTION_DISABLE_THEN_DELETE,
							Slow:   true,
						},
						{Kind: "AWS::CloudFront::KeyValueStore", Name: "shop.example.com", Action: contractv1.Change_ACTION_DELETE},
					},
				},
				{Kind: "certificate", Name: "shop.example.com", Action: contractv1.Change_ACTION_KEEP, Reason: "you pinned this certificate"},
			},
		})
		got := out.String()
		for _, want := range []string{
			`production project "proj_shop"`,
			"fronted by the cloudfront edge",
			"– aws/shop--infra  [infra]  — databases and buckets, INCLUDING ALL DATA",
			"– aws/shop--web--b1  [web]",
			"– cloudfront/edge",
			"    – disable, then delete E1SHOP  AWS::CloudFront::Distribution (this one is slow)",
			"    – shop.example.com             AWS::CloudFront::KeyValueStore",
			"every production variable value",
			"This cannot be undone.",
			"Left in place:",
			"  certificate shop.example.com  — you pinned this certificate",
			"4 to delete.",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("printDestroyPlan output missing %q; got:\n%s", want, got)
			}
		}
		if strings.Index(got, "  certificate") < strings.Index(got, "This cannot be undone.") {
			t.Errorf("printDestroyPlan listed a kept item among the doomed ones; got:\n%s", got)
		}
	})

	t.Run("with no edge bought the quota-paced deletions read as slow", func(t *testing.T) {
		t.Parallel()

		var out bytes.Buffer
		printDestroyPlan(&out, "proj_shop", false, &contractv1.ChangePlan{
			EdgeKind: "api-gateway",
			Groups: []*contractv1.ChangeGroup{{
				Kind:   "edge",
				Name:   "api-gateway/edge",
				Action: contractv1.Change_ACTION_DELETE,
				Changes: []*contractv1.Change{
					{Kind: "AWS::ApiGateway::RestApi", Name: "shop", Action: contractv1.Change_ACTION_DELETE, Slow: true},
					{Kind: "AWS::ApiGateway::DomainName", Name: "shop.example.com", Action: contractv1.Change_ACTION_DELETE},
				},
			}},
		})
		got := out.String()
		if !strings.Contains(got, "– shop              AWS::ApiGateway::RestApi (this one is slow)") {
			t.Errorf("printDestroyPlan output missing the slow REST API row; got:\n%s", got)
		}
		if !strings.Contains(got, "– shop.example.com  AWS::ApiGateway::DomainName\n") {
			t.Errorf("printDestroyPlan marked an unpaced row slow; got:\n%s", got)
		}
	})

	t.Run("an action this CLI does not know reads as a sentence", func(t *testing.T) {
		t.Parallel()

		got := changeplan.NewPrinter(io.Discard).GroupLine(&contractv1.ChangeGroup{
			Kind:   "certificate",
			Name:   "shop.example.com",
			Action: contractv1.Change_Action(97),
		})
		if !strings.Contains(got, "an action this CLI does not know") || !strings.HasSuffix(got, "certificate shop.example.com") {
			t.Errorf("changeplan.GroupLine() = %q, want the unknown action named before the resource", got)
		}
	})
}

func TestRunDestroyPreviewProject(t *testing.T) {
	t.Run("--yes skips the terminal check and the typed name", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDestroyPreviewProject(context.Background(), deps, root, true, false, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyPreviewProject err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, want := range []string{
			`ENTIRE preview footprint of project "test-app"`,
			"fronted by the cloudfront edge",
			"– aws/test-app--pr-1--infra  [infra]  — databases and buckets, INCLUDING ALL DATA",
			"– aws/test-app--pr-2--infra  [infra]",
			"– aws/test-app--pr-1--web--b1  [web]",
			"– cloudfront/edge",
			"    – test-app  AWS::CloudFront::Distribution",
			"every preview variable value",
			"The account-level preview bootstrap is left intact. This cannot be undone.",
			"Left in place:",
			"  cloudfront/edge  — bootstrap-scoped: every project's previews are served on *.preview.acme.com",
			"DESTROY PROJECT project=test-app dns= tier=TIER_PREVIEW",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		if strings.Index(out, "Left in place:") < strings.Index(out, "This cannot be undone.") {
			t.Errorf("stdout listed a kept item among the doomed ones:\n%s", out)
		}
		if strings.Contains(out, "Type the project name") {
			t.Errorf("stdout = %q, want --yes to skip the typed-name confirmation", out)
		}
	})

	t.Run("the dns descriptor rides along so the teardown can delete what it wrote", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
  dns: { kind: "route53" },
};
`)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDestroyPreviewProject(context.Background(), deps, root, true, false, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyPreviewProject err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if out := stdout.String(); !strings.Contains(out, "DESTROY PROJECT project=test-app dns=route53") {
			t.Errorf("stdout = %q, want the dns descriptor on the teardown request", out)
		}
	})

	t.Run("--dry prints the plan, tears nothing down, and needs no terminal", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "preview")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDestroyPreviewProject(context.Background(), deps, root, true, true, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyPreviewProject err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, `ENTIRE preview footprint of project "test-app"`) {
			t.Errorf("stdout = %q, want --dry to print the plan", out)
		}
		if !strings.Contains(out, "Run without --dry to destroy.") {
			t.Errorf("stdout = %q, want --dry to say how to destroy it", out)
		}
		if strings.Contains(out, "DESTROY PROJECT") {
			t.Errorf("stdout = %q, want --dry to beat --yes and destroy nothing", out)
		}
		if got := clitest.ReadJournal(t, journal); len(got) != 1 {
			t.Errorf("provider saw %v, want the plan alone", got)
		}
	})

	t.Run("without --yes it refuses without a terminal", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := runDestroyPreviewProject(context.Background(), newDeps(), t.TempDir(), false, false, &stdout, &stderr, strings.NewReader(""))
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
		err := runDestroyProduction(context.Background(), newDeps(), t.TempDir(), false, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDestroyProduction without a TTY err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "interactive terminal") {
			t.Errorf("err = %v, want the no-TTY refusal", err)
		}
	})

	t.Run("the project name gets past the terminal requirement and says so", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(changeplan.BypassEnv, "test-app")

		var stdout, stderr bytes.Buffer
		err := runDestroyProduction(context.Background(), deps, root, false, &stdout, &stderr, strings.NewReader(""))
		if err != nil && strings.Contains(err.Error(), "interactive terminal") {
			t.Errorf("err = %v, want the bypass to get past the TTY requirement", err)
		}
		if strings.Contains(stdout.String(), "Type the project name") {
			t.Errorf("stdout = %q, want the bypass to skip the typed-name confirmation", stdout.String())
		}
		if !strings.Contains(stderr.String(), changeplan.BypassEnv) {
			t.Errorf("stderr = %q, want it to name %s so an unconfirmed destroy is never silent", stderr.String(), changeplan.BypassEnv)
		}
	})

	t.Run("it renders the plan the provider sent, kept items included", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(changeplan.BypassEnv, "test-app")

		var stdout, stderr bytes.Buffer
		if err := runDestroyProduction(context.Background(), deps, root, false, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyProduction err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, want := range []string{
			"fronted by the cloudfront edge",
			"– cloudfront/edge",
			"    – disable, then delete E1test-app  AWS::CloudFront::Distribution (this one is slow)",
			"  certificate test-app.example.com  — you pinned this certificate; Ocel never deletes one it did not request",
			"– aws/test-app--infra  [infra]",
			"– aws/test-app--web--b1  [web]",
			"DESTROY PROJECT project=test-app",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, out)
			}
		}
	})

	t.Run("--dry prints the plan, destroys nothing, and needs no terminal", func(t *testing.T) {
		root, journal, deps := clitest.SetUpEdgeFixture(t, "")
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDestroyProduction(context.Background(), deps, root, true, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyProduction err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, `This will permanently destroy production project "test-app"`) {
			t.Errorf("stdout = %q, want --dry to print the plan", out)
		}
		if !strings.Contains(out, "Run without --dry to destroy.") {
			t.Errorf("stdout = %q, want --dry to say how to destroy it", out)
		}
		if strings.Contains(out, "DESTROY PROJECT") {
			t.Errorf("stdout = %q, want --dry to destroy nothing", out)
		}
		if got := clitest.ReadJournal(t, journal); len(got) != 1 {
			t.Errorf("provider saw %v, want the plan alone", got)
		}
	})

	t.Run("an empty plan destroys nothing and never asks for the project name", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(clitest.FakeInfraTierEnvVar, "production")
		t.Setenv(clitest.FakeInfraPresentEnvVar, "1")
		t.Setenv(clitest.FakeEmptyRemovalPlanEnvVar, "1")
		t.Setenv(changeplan.BypassEnv, "test-app")

		var stdout, stderr bytes.Buffer
		if err := runDestroyProduction(context.Background(), deps, root, false, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDestroyProduction err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "Nothing to destroy") {
			t.Errorf("stdout = %q, want it to say nothing was there to destroy", out)
		}
		for _, unwanted := range []string{"This will permanently destroy", "DESTROY PROJECT"} {
			if strings.Contains(out, unwanted) {
				t.Errorf("stdout = %q, want no %q: an empty plan must stop before the plan is rendered", out, unwanted)
			}
		}
	})

	t.Run("a value that is not this project's name is refused without a terminal", func(t *testing.T) {
		root, _ := clitest.SetUpDeployFixture(t)
		deps := newDeps()
		clitest.SetLoggedIn(&deps)
		clitest.StubBuild(&deps, nil)
		t.Setenv(changeplan.BypassEnv, "1")

		var stdout, stderr bytes.Buffer
		err := runDestroyProduction(context.Background(), deps, root, false, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatalf("runDestroyProduction err = nil, want an ambient %s=1 refused; stdout=%s", changeplan.BypassEnv, stdout.String())
		}
		if !strings.Contains(err.Error(), changeplan.BypassEnv) || !strings.Contains(err.Error(), "test-app") {
			t.Errorf("err = %v, want it to name %s and the project", err, changeplan.BypassEnv)
		}
	})

	t.Run("an unset bypass is not a bypass", func(t *testing.T) {
		t.Setenv(changeplan.BypassEnv, "")
		var stdout, stderr bytes.Buffer
		err := runDestroyProduction(context.Background(), newDeps(), t.TempDir(), false, &stdout, &stderr, strings.NewReader(""))
		if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
			t.Errorf("err = %v, want the no-TTY refusal", err)
		}
	})
}

func TestDestroyNeedsAClass(t *testing.T) {
	var out bytes.Buffer
	destroyCmd.SetOut(&out)
	destroyCmd.SetErr(&out)
	t.Cleanup(func() { destroyCmd.SetOut(nil); destroyCmd.SetErr(nil) })

	if err := destroyCmd.RunE(destroyCmd, nil); err == nil {
		t.Fatal("bare destroy err = nil, want destroy without a class to be a failure")
	}
	for _, want := range []string{"production", "preview"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output = %q, want the help to list %q", out.String(), want)
		}
	}
}

func TestDestroyNamesAClassItDoesNotKnow(t *testing.T) {
	var out bytes.Buffer
	destroyCmd.SetOut(&out)
	destroyCmd.SetErr(&out)
	t.Cleanup(func() { destroyCmd.SetOut(nil); destroyCmd.SetErr(nil) })

	err := destroyCmd.RunE(destroyCmd, []string{"foo"})
	if err == nil {
		t.Fatal("destroy foo err = nil, want a class it does not know to be a failure")
	}
	if err.Error() != `the class to destroy is production or preview, not "foo"` {
		t.Errorf("err = %v, want it to name the value typed", err)
	}
	if out.Len() != 0 {
		t.Errorf("output = %q, want no help dump when the class is named but wrong", out.String())
	}
}

func TestDestroyClassCommands(t *testing.T) {
	for typed, want := range map[string]string{
		"production": "production",
		"prod":       "production",
		"preview":    "preview",
	} {
		found, _, err := destroyCmd.Find([]string{typed})
		if err != nil {
			t.Fatalf("Find(%q) err = %v", typed, err)
		}
		if found.Name() != want {
			t.Errorf("Find(%q) = %q, want %q", typed, found.Name(), want)
		}
	}

	production, _, _ := destroyCmd.Find([]string{"production"})
	if production.Flags().Lookup("yes") != nil {
		t.Error("destroy production carries --yes; production is confirmed by the typed project name alone")
	}
	preview, _, _ := destroyCmd.Find([]string{"preview"})
	if preview.Flags().Lookup("yes") == nil {
		t.Error("destroy preview carries no --yes")
	}
	for _, cmd := range []*cobra.Command{production, preview} {
		if cmd.Flags().Lookup("dry") == nil {
			t.Errorf("destroy %s carries no --dry; every destructive command previews", cmd.Name())
		}
	}
}
