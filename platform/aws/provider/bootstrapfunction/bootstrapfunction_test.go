package bootstrapfunction_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/platform/aws/provider/bootstrapfunction"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrapfunction/functiontest"
)

func function(code string) bootstrapfunction.Function {
	return bootstrapfunction.Function{
		Name:           "ocel-thing",
		Description:    "a thing",
		Runtime:        "nodejs24.x",
		Architecture:   "arm64",
		Handler:        "index.handler",
		MemoryMB:       512,
		TimeoutSeconds: 30,
		Role:           functiontest.RoleARN("/ocel/production/app/", "ocel-thing"),
		Code:           []byte(code),
		Variables:      map[string]string{"A": "1"},
		LogGroup:       "/aws/lambda/ocel-thing",
		RetentionDays:  14,
		Tags:           map[string]string{"ocel:managed-by": "ocel"},
	}
}

func TestEnsureMakesTheFunctionAndTheLogGroupItWritesTo(t *testing.T) {
	t.Parallel()
	account := functiontest.New()
	account.Unassumable = 2

	if err := bootstrapfunction.Ensure(context.Background(), account.Clients(), function("one")); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}

	made, ok := account.Functions["ocel-thing"]
	if !ok {
		t.Fatalf("no function made")
	}
	if string(made.Code) != "one" || aws.ToString(made.Input.LoggingConfig.LogGroup) != "/aws/lambda/ocel-thing" || made.Tags["ocel:managed-by"] != "ocel" {
		t.Errorf("function = %+v, want its code, its log group and its tags", made)
	}
	if account.Retention["/aws/lambda/ocel-thing"] != 14 {
		t.Errorf("log group retention = %v, want 14 days", account.Retention)
	}
}

func TestEnsureMovesAnExistingFunctionToTheCodeAndConfigurationAsked(t *testing.T) {
	t.Parallel()
	account := functiontest.New()
	if err := bootstrapfunction.Ensure(context.Background(), account.Clients(), function("one")); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
	account.Updating = 2
	next := function("two")
	next.MemoryMB = 1024

	if err := bootstrapfunction.Ensure(context.Background(), account.Clients(), next); err != nil {
		t.Fatalf("Ensure() again = %v", err)
	}

	made := account.Functions["ocel-thing"]
	if !bytes.Equal(made.Code, []byte("two")) || aws.ToInt32(made.Input.MemorySize) != 1024 {
		t.Errorf("function code %q memory %d, want the second run's", made.Code, aws.ToInt32(made.Input.MemorySize))
	}
}

func TestEnsureRoleMakesARoleUnderItsBoundaryAndPutsItsPolicy(t *testing.T) {
	t.Parallel()
	account := functiontest.New()
	role := bootstrapfunction.Role{
		Name: "ocel-thing", Path: "/ocel/production/app/", Boundary: "arn:aws:iam::123456789012:policy/b",
		Description: "d", Service: "lambda.amazonaws.com", PolicyName: "p", Policy: `{"Statement":[]}`,
		Tags: map[string]string{"ocel:env-tier": "production"},
	}

	for range 2 {
		if err := bootstrapfunction.EnsureRole(context.Background(), account.Clients().IAM, role); err != nil {
			t.Fatalf("EnsureRole() = %v", err)
		}
	}

	made, ok := account.Roles["ocel-thing"]
	if !ok {
		t.Fatalf("no role made")
	}
	if made.Path != role.Path || made.Boundary != role.Boundary || made.Policies["p"] != role.Policy || made.Tags["ocel:env-tier"] != "production" {
		t.Errorf("role = %+v, want it at its path under its boundary with its policy and tags", made)
	}
}

func TestRemoveDeletesWhatEnsureMadeAndIgnoresWhatIsAlreadyGone(t *testing.T) {
	t.Parallel()
	account := functiontest.New()
	if err := bootstrapfunction.Ensure(context.Background(), account.Clients(), function("one")); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}

	for range 2 {
		if err := bootstrapfunction.Remove(context.Background(), account.Clients(), "ocel-thing", "/aws/lambda/ocel-thing"); err != nil {
			t.Fatalf("Remove() = %v", err)
		}
	}

	if len(account.Functions) != 0 || len(account.LogGroups) != 0 {
		t.Errorf("functions %v log groups %v remain, want none", account.Functions, account.LogGroups)
	}
}
