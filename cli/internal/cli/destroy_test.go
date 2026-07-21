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
		"This cannot be undone.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("printDestroyPlan output missing %q; got:\n%s", want, got)
		}
	}
}

// TestRunDestroy_RefusesWithoutTTY proves destroy will not run when stdin is not
// an interactive terminal — the only confirmation is typing the project name,
// and it must never be bypassable. It refuses before resolving config or
// spawning the provider.
func TestRunDestroy_RefusesWithoutTTY(t *testing.T) {
	setLoggedIn(t)

	var stdout, stderr bytes.Buffer
	err := runDestroy(context.Background(), t.TempDir(), &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDestroy without a TTY err = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "interactive terminal") {
		t.Errorf("err = %v, want the no-TTY refusal", err)
	}
}
