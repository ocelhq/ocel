package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// TestConfirmDestroyProject covers the typed-name gate directly: only the exact
// project name proceeds, so a slip or a reflexive "y" never nukes production.
func TestConfirmDestroyProject(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"exact name proceeds", "proj_shop\n", true},
		{"exact name with surrounding space proceeds", "  proj_shop  \n", true},
		{"wrong name aborts", "proj_shopp\n", false},
		{"reflexive yes aborts", "y\n", false},
		{"empty aborts", "\n", false},
		{"closed stdin aborts", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			got, err := confirmDestroyProject("proj_shop", &stdout, strings.NewReader(tc.input))
			if err != nil {
				t.Fatalf("confirmDestroyProject() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("confirmDestroyProject(%q) = %v, want %v", tc.input, got, tc.want)
			}
			if !strings.Contains(stdout.String(), "Type the project name (proj_shop) to confirm:") {
				t.Errorf("stdout = %q, want the typed-name prompt", stdout.String())
			}
		})
	}
}

func TestDestroyPlanEmpty(t *testing.T) {
	if !destroyPlanEmpty(&deploymentsv1.PlanDestroyProjectResponse{}) {
		t.Error("an all-empty plan should be empty")
	}
	if destroyPlanEmpty(&deploymentsv1.PlanDestroyProjectResponse{RootStack: true}) {
		t.Error("a plan with a root stack is not empty")
	}
	if destroyPlanEmpty(&deploymentsv1.PlanDestroyProjectResponse{InfraStack: "shop--infra"}) {
		t.Error("a plan with an infra stack is not empty")
	}
	if destroyPlanEmpty(&deploymentsv1.PlanDestroyProjectResponse{AppStacks: []string{"shop--web--b1"}}) {
		t.Error("a plan with app stacks is not empty")
	}
}

func TestPrintDestroyPlan_ListsEveryTarget(t *testing.T) {
	var out bytes.Buffer
	printDestroyPlan(&out, "proj_shop", &deploymentsv1.PlanDestroyProjectResponse{
		RootStack:  true,
		InfraStack: "shop--infra",
		AppStacks:  []string{"shop--web--b1", "shop--api--b2"},
	})
	got := out.String()
	for _, want := range []string{
		`production project "proj_shop"`,
		"root stack",
		"infra stack shop--infra",
		"INCLUDING ALL DATA",
		"app stack shop--web--b1",
		"app stack shop--api--b2",
		"every production variable value",
		"This cannot be undone.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("printDestroyPlan output missing %q; got:\n%s", want, got)
		}
	}
}

// TestCheckDestroyFlags_YesIsPreviewOnly pins the asymmetry: --yes automates a
// project's preview footprint, which is disposable by construction, and is
// refused for production, whose only confirmation is typing the project name.
func TestCheckDestroyFlags_YesIsPreviewOnly(t *testing.T) {
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
}

// TestRunDestroyPreviewProject_YesSkipsTTYAndTypedName proves the
// non-interactive teardown path the e2e sweeper and the workflow's destroy job
// need: --yes skips both the terminal requirement and the typed-name prompt.
func TestRunDestroyPreviewProject_YesSkipsTTYAndTypedName(t *testing.T) {
	root, _ := setUpDeployFixture(t)
	t.Setenv(fakeInfraClassEnvVar, "preview")
	t.Setenv(fakeInfraPresentEnvVar, "1")

	var stdout, stderr bytes.Buffer
	if err := runDestroyPreviewProject(context.Background(), root, true, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDestroyPreviewProject err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "DESTROY PROJECT project=test-app class=CLASS_PREVIEW") {
		t.Errorf("stdout = %q, want the preview DestroyProject echo", out)
	}
	if strings.Contains(out, "Type the project name") {
		t.Errorf("stdout = %q, want --yes to skip the typed-name confirmation", out)
	}
}

// TestRunDestroyPreviewProject_WithoutYesRefusesWithoutTTY keeps the default
// safe: without --yes there is still no non-interactive path.
func TestRunDestroyPreviewProject_WithoutYesRefusesWithoutTTY(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runDestroyPreviewProject(context.Background(), t.TempDir(), false, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDestroyPreviewProject without a TTY err = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "interactive terminal") {
		t.Errorf("err = %v, want the no-TTY refusal", err)
	}
}

// TestRunDestroy_RefusesWithoutTTY proves destroy will not run when stdin is not
// an interactive terminal — the only confirmation is typing the project name,
// and it must never be bypassable. It refuses before resolving config or
// spawning the provider.
func TestRunDestroy_RefusesWithoutTTY(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runDestroy(context.Background(), t.TempDir(), &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDestroy without a TTY err = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "interactive terminal") {
		t.Errorf("err = %v, want the no-TTY refusal", err)
	}
}
