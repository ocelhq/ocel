package lambdapart_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/lambdapart"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/lambdapart/functiontest"
)

func function(code string) lambdapart.Function {
	return lambdapart.Function{
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

	if err := lambdapart.Ensure(context.Background(), account.Clients(), function("one")); err != nil {
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
	if err := lambdapart.Ensure(context.Background(), account.Clients(), function("one")); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
	account.Updating = 2
	next := function("two")
	next.MemoryMB = 1024

	if err := lambdapart.Ensure(context.Background(), account.Clients(), next); err != nil {
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
	role := lambdapart.Role{
		Name: "ocel-thing", Path: "/ocel/production/app/", Boundary: "arn:aws:iam::123456789012:policy/b",
		Description: "d", Service: "lambda.amazonaws.com", PolicyName: "p", Policy: `{"Statement":[]}`,
		Tags: map[string]string{"ocel:env-tier": "production"},
	}

	for range 2 {
		if err := lambdapart.EnsureRole(context.Background(), account.Clients().IAM, role); err != nil {
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

func driftedRole(t *testing.T) (role lambdapart.Role, freshTrust string, account *functiontest.Account, id int) {
	t.Helper()
	role = lambdapart.Role{
		Name: "ocel-thing", Path: "/ocel/production/app/", Boundary: "arn:aws:iam::123456789012:policy/b",
		Description: "d", Service: "lambda.amazonaws.com", PolicyName: "p", Policy: `{"Statement":[]}`,
		Tags: map[string]string{"ocel:env-tier": "production"},
	}
	fresh := functiontest.New()
	if err := lambdapart.EnsureRole(context.Background(), fresh.Clients().IAM, role); err != nil {
		t.Fatal(err)
	}
	account = functiontest.New()
	if err := lambdapart.EnsureRole(context.Background(), account.Clients().IAM, role); err != nil {
		t.Fatal(err)
	}
	if err := lambdapart.Ensure(context.Background(), account.Clients(), function("one")); err != nil {
		t.Fatal(err)
	}
	account.Roles["ocel-thing"].Trust = `{"Version":"2012-10-17","Statement":[]}`
	account.Roles["ocel-thing"].Boundary = "arn:aws:iam::123456789012:policy/an-older-boundary"
	account.Roles["ocel-thing"].Policies["p"] = `{"Statement":["an older policy"]}`
	return role, fresh.Roles["ocel-thing"].Trust, account, account.Roles["ocel-thing"].ID
}

func keptTheRole(t *testing.T, account *functiontest.Account, id int) {
	t.Helper()
	if account.Called("DeleteRole") > 0 || account.Roles["ocel-thing"] == nil || account.Roles["ocel-thing"].ID != id {
		t.Errorf("calls = %v, want the role kept: a role made again is a new identity", account.Calls)
	}
	if !account.RunsAsItsRole("ocel-thing") {
		t.Error("the function no longer runs as the role it was given, so Lambda's grant to decrypt its variables is gone")
	}
}

func TestEnsureRoleWithoutUpdatesTrustLeavesADriftedTrustPolicyAndSaysThePartIsBehind(t *testing.T) {
	t.Parallel()
	role, freshTrust, account, id := driftedRole(t)

	err := lambdapart.EnsureRole(context.Background(), account.Clients().IAM, role)
	if !errors.Is(err, stackrecords.ErrPartBehind) {
		t.Fatalf("EnsureRole() over a role whose trust drifted = %v, want the part said to be behind", err)
	}

	keptTheRole(t, account, id)
	got := account.Roles["ocel-thing"]
	if account.Called("UpdateAssumeRolePolicy") > 0 || got.Trust == freshTrust {
		t.Error("a deploy changed a role's trust policy")
	}
	if got.Boundary != role.Boundary || got.Policies["p"] != role.Policy {
		t.Errorf("role under %s with policy %s, want its boundary and policy put back", got.Boundary, got.Policies["p"])
	}
}

func TestEnsureRoleWithUpdatesTrustPutsBackTheTrustPolicyAndBoundaryInPlace(t *testing.T) {
	t.Parallel()
	role, freshTrust, account, id := driftedRole(t)
	role.UpdatesTrust = true

	if err := lambdapart.EnsureRole(context.Background(), account.Clients().IAM, role); err != nil {
		t.Fatalf("EnsureRole() over a drifted role = %v", err)
	}

	keptTheRole(t, account, id)
	got := account.Roles["ocel-thing"]
	if got.Trust != freshTrust || got.Boundary != role.Boundary || got.Policies["p"] != role.Policy {
		t.Errorf("role trusts %s under %s, want %s under %s with its policy, as a fresh role's", got.Trust, got.Boundary, freshTrust, role.Boundary)
	}
}

func TestRemoveDeletesWhatEnsureMadeAndIgnoresWhatIsAlreadyGone(t *testing.T) {
	t.Parallel()
	account := functiontest.New()
	if err := lambdapart.Ensure(context.Background(), account.Clients(), function("one")); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}

	for range 2 {
		if err := lambdapart.Remove(context.Background(), account.Clients(), "ocel-thing", "/aws/lambda/ocel-thing"); err != nil {
			t.Fatalf("Remove() = %v", err)
		}
	}

	if len(account.Functions) != 0 || len(account.LogGroups) != 0 {
		t.Errorf("functions %v log groups %v remain, want none", account.Functions, account.LogGroups)
	}
}
