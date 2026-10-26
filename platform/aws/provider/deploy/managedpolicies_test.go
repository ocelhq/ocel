package deploy

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	awsresources "github.com/ocelhq/ocel/platform/aws/provider/resources"

	"github.com/ocelhq/ocel/platform/aws/provider/compute/worker"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
)

const grantedPolicyARN = "arn:aws:iam::123456789012:policy/reports-read"

func attachingRolePolicies(arns ...any) patchingPass {
	return patchingPass{patch: func(patches []transform.Patches) {
		for _, patch := range patches {
			patch["role"] = map[string]any{"managedPolicyArns": arns}
		}
	}}
}

func TestATransformAttachesManagedPoliciesToAFunctionRoleBesideOcelsOwn(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	transformed, err := transformStackSpec(context.Background(), testNamespace, testAccount, attachingRolePolicies(grantedPolicyARN, iam.LambdaBasicExecutionPolicyARN), awsresources.Registry(awsresources.Clients{}), spec)
	if err != nil {
		t.Fatalf("transformStackSpec() = %v", err)
	}
	work, err := releasing(t, cfg).appWork(spec, transformed, nil, variables.Bundle{}, stagedOf(spec))
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}

	rec := runAppProgram(t, work)

	for _, arn := range []string{grantedPolicyARN, iam.LambdaBasicExecutionPolicyARN} {
		if n := countOf(rec.attached, arn); n != 1 {
			t.Errorf("attached %s %d times among %v, want once: a patched policy joins ocel's own, and one already attached is not attached again", arn, n, rec.attached)
		}
	}
	role := mustFunctionNames(t, spec.Ref.Name, spec.App.Functions[0].Name)["role"][0]
	if patch, _ := transformed.patched.For(role); patch["managedPolicyArns"] != nil {
		t.Errorf("the role's patch sets managedPolicyArns, which would detach every policy ocel attaches beside them")
	}
}

func TestTheWorkersOfAnAppCarryTheManagedPoliciesATransformAttachesToItsFunctions(t *testing.T) {
	t.Parallel()

	release, spec, _, _ := newReleaseHostingWorkers(t, "media")
	transformed, err := transformStackSpec(context.Background(), testNamespace, testAccount, attachingRolePolicies(grantedPolicyARN), awsresources.Registry(awsresources.Clients{}), spec)
	if err != nil {
		t.Fatalf("transformStackSpec() = %v", err)
	}
	work, err := release.appWork(spec, transformed, nil, variables.Bundle{}, stagedOf(spec))
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}
	hosted, err := worker.NewStack(spec, work.functions, work.role, work.network, release.cfg.workerTarget())
	if err != nil {
		t.Fatalf("worker.NewStack() = %v", err)
	}

	rec := &inputRecorder{}
	if err := pulumi.RunErr(hosted.Declare, pulumi.WithMocks("shop", "prod-workers", rec)); err != nil {
		t.Fatalf("run the workers program: %v", err)
	}

	if !slices.Contains(rec.attached, grantedPolicyARN) {
		t.Errorf("attached %v, want %s: the workers run the app's code, so they reach what the app was granted", rec.attached, grantedPolicyARN)
	}
}

func TestATransformAttachesManagedPoliciesToAContainerTaskRoleItMintsForThem(t *testing.T) {
	t.Parallel()

	cfg, spec := containerStackSpec(t)
	cfg.Transform = attachingRolePolicies(grantedPolicyARN)
	work, err := containerDefinitionOf(cfg, spec)
	if err != nil {
		t.Fatalf("containerDefinitionOf() = %v", err)
	}
	transformed, err := releasing(t, cfg).transformContainer(context.Background(), spec, work)
	if err != nil {
		t.Fatalf("transformContainer() = %v", err)
	}
	if err := work.PlaceRule(context.Background(), cfg.Rules); err != nil {
		t.Fatalf("PlaceRule() = %v", err)
	}

	rec := &inputRecorder{}
	if err := pulumi.RunErr(releasing(t, cfg).containerProgram(spec, work, transformed).Run, pulumi.WithMocks("shop", spec.Ref.Name.String(), rec)); err != nil {
		t.Fatalf("run the container program: %v", err)
	}

	if !slices.Contains(rec.attached, grantedPolicyARN) {
		t.Errorf("attached %v, want the patched policy on the task role, minted for it though the app binds nothing", rec.attached)
	}
	if roles := rec.registered("aws:iam/role:Role"); len(roles) != 1 {
		t.Errorf("declared roles %v, want the task role alone", roles)
	}
}

func TestTwoManagedPoliciesOfOneNameUnderDifferentPathsAreRefusedRatherThanCollide(t *testing.T) {
	t.Parallel()

	cfg, spec := appStackSpec(t)
	const team, shared = "arn:aws:iam::123456789012:policy/team/reports-read", "arn:aws:iam::123456789012:policy/shared/reports-read"
	transformed, err := transformStackSpec(context.Background(), testNamespace, testAccount, attachingRolePolicies(team, shared), awsresources.Registry(awsresources.Clients{}), spec)
	if err != nil {
		t.Fatalf("transformStackSpec() = %v", err)
	}
	work, err := releasing(t, cfg).appWork(spec, transformed, nil, variables.Bundle{}, stagedOf(spec))
	if err != nil {
		t.Fatalf("appWork() = %v", err)
	}

	err = pulumi.RunErr(func(pctx *pulumi.Context) error { return work.run(pctx, nil) }, pulumi.WithMocks("shop", "prod", &inputRecorder{}))
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, team) || !strings.Contains(refused.Message, shared) {
		t.Fatalf("run the app program = %v, want a refusal naming both policies", err)
	}
}

func TestAManagedPolicyThatIsNotAnARNIsRefusedByName(t *testing.T) {
	t.Parallel()

	_, spec := appStackSpec(t)
	_, err := transformStackSpec(context.Background(), testNamespace, testAccount, attachingRolePolicies(float64(7)), awsresources.Registry(awsresources.Clients{}), spec)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "managedPolicyArns") {
		t.Fatalf("transformStackSpec() = %v, want a refusal naming managedPolicyArns", err)
	}
}

func TestAManagedPolicyTheDeployCannotAttachIsRefusedByNameBeforeThePlan(t *testing.T) {
	t.Parallel()

	_, spec := appStackSpec(t)
	for _, arn := range []string{
		"arn:aws:iam::aws:policy/AdministratorAccess",
		"arn:aws:iam::210987654321:policy/reports-read",
		"arn:aws:iam::123456789012:role/reports-read",
	} {
		_, err := transformStackSpec(context.Background(), testNamespace, testAccount, attachingRolePolicies(arn), awsresources.Registry(awsresources.Clients{}), spec)
		var refused refusal.Refusal
		if !errors.As(err, &refused) || !strings.Contains(refused.Message, arn) || !strings.Contains(refused.Message, "arn:aws:iam::"+testAccount+":policy/") {
			t.Errorf("transformStackSpec(%s) = %v, want a refusal naming the policy and the account whose policies a role takes", arn, err)
		}
	}
}

func countOf(values []string, want string) int {
	return len(slices.DeleteFunc(slices.Clone(values), func(value string) bool { return value != want }))
}
