package bastion_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
)

func readyBastion(t *testing.T, account *account) (bastion.Clients, bastion.Bastion) {
	t.Helper()
	clients := account.clients()
	reconciled, err := bastion.Reconcile(context.Background(), clients, testSpec)
	if err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}
	return clients, reconciled
}

func TestRunStartsATaskInAPublicSubnetBehindTheBastionsSecurityGroupAndWaitsForTheExecAgent(t *testing.T) {
	t.Parallel()

	account := newAccount()
	account.agentAfter = 2
	clients, reconciled := readyBastion(t, account)

	started, err := reconciled.Run(context.Background(), clients)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}

	running := account.runningTasks()
	if len(running) != 1 {
		t.Fatalf("running tasks = %v, want exactly the one Run() started", running)
	}
	launched := account.tasks[running[0]]
	if !launched.public || !launched.exec || len(launched.groups) != 1 || launched.groups[0] != reconciled.SecurityGroup {
		t.Errorf("the task = %+v, want a public IP for egress, ECS Exec on and only the bastion's security group %s", launched, reconciled.SecurityGroup)
	}
	if len(launched.subnets) != 2 || launched.definition != reconciled.TaskDefinition {
		t.Errorf("the task runs %s in %v, want the bastion's task definition in the default subnets", launched.definition, launched.subnets)
	}
	id := running[0][strings.LastIndex(running[0], "/")+1:]
	if want := "ecs:ocel-prod-bastion_" + id + "_runtime-" + id; started.ManagedNode != want {
		t.Errorf("Run().ManagedNode = %q, want %q, the ECS target Session Manager resolves", started.ManagedNode, want)
	}
	if account.describeCalls <= 2 {
		t.Errorf("DescribeTasks was called %d times, want Run() to wait past the 2 polls before the agent runs", account.describeCalls)
	}
}

func TestRunStopsTheTaskAndSaysWhyWhenItStopsBeforeItsExecAgentRuns(t *testing.T) {
	t.Parallel()

	account := newAccount()
	account.agentAfter = 100
	account.onDescribe = func(t *task) { t.stopped = true }
	clients, reconciled := readyBastion(t, account)

	_, err := reconciled.Run(context.Background(), clients)

	if err == nil || !strings.Contains(err.Error(), "stopped in the test") {
		t.Fatalf("Run() = %v, want an error that gives the task's stopped reason", err)
	}
	if running := account.runningTasks(); len(running) != 0 {
		t.Errorf("tasks %v are still running", running)
	}
}

func TestRunStopsTheTaskItStartedWhenItsContextEndsWhileWaiting(t *testing.T) {
	t.Parallel()

	account := newAccount()
	account.agentAfter = 1_000_000
	clients, reconciled := readyBastion(t, account)
	ctx, cancel := context.WithCancel(context.Background())
	account.onDescribe = func(*task) { cancel() }

	_, err := reconciled.Run(ctx, clients)

	if err == nil {
		t.Fatal("Run() with a cancelled context = nil error")
	}
	if running := account.runningTasks(); len(running) != 0 {
		t.Errorf("tasks %v are still running: a cancelled forward must not leave a task billing", running)
	}
}

func TestRunStartsAgainWhenECSCannotAssumeARoleIAMHasJustCreated(t *testing.T) {
	t.Parallel()

	account := newAccount()
	account.runFails = []string{"ECS was unable to assume the role 'arn:aws:iam::123456789012:role/ocel-prod-bastion' that was provided for this task."}
	clients, reconciled := readyBastion(t, account)

	if _, err := reconciled.Run(context.Background(), clients); err != nil {
		t.Fatalf("Run() = %v, want it to wait out IAM's propagation", err)
	}

	if calls := account.called("RunTask"); len(calls) != 2 {
		t.Errorf("RunTask was called %d times, want 2: one refused for the new role, one that started", len(calls))
	}
}

func TestRunStartsAgainWhileECSCannotYetAssumeItsServiceLinkedRole(t *testing.T) {
	t.Parallel()

	account := newAccount()
	clients, reconciled := readyBastion(t, account)
	delete(account.linkedRoles, "ecs.amazonaws.com")
	notYet := &ecstypes.InvalidParameterException{Message: aws.String("Unable to assume the service linked role. Please verify that the ECS service linked role exists.")}
	account.runErrors = []error{notYet, notYet}

	if _, err := reconciled.Run(context.Background(), clients); err != nil {
		t.Fatalf("Run() = %v, want it to wait out IAM's propagation of ECS's service-linked role", err)
	}

	if calls := account.called("RunTask"); len(calls) != 3 {
		t.Errorf("RunTask was called %d times, want 3: two refused while the role propagates, one that started", len(calls))
	}
	if !account.linkedRoles["ecs.amazonaws.com"] {
		t.Error("Run() did not create ECS's service-linked role that RunTask said is missing")
	}
}

func TestRunReportsATaskECSRefusesToStartForAnyOtherReason(t *testing.T) {
	t.Parallel()

	account := newAccount()
	account.runFails = []string{"RESOURCE:FARGATE"}
	clients, reconciled := readyBastion(t, account)

	_, err := reconciled.Run(context.Background(), clients)

	if err == nil || !strings.Contains(err.Error(), "RESOURCE:FARGATE") {
		t.Fatalf("Run() = %v, want the reason ECS gave", err)
	}
	if calls := account.called("RunTask"); len(calls) != 1 {
		t.Errorf("RunTask was called %d times, want 1: nothing about this refusal changes by asking again", len(calls))
	}
}

func TestStopEndsTheTaskOnceHoweverOftenItIsCalled(t *testing.T) {
	t.Parallel()

	account := newAccount()
	clients, reconciled := readyBastion(t, account)
	started, err := reconciled.Run(context.Background(), clients)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}

	for range 2 {
		if err := started.Stop(); err != nil {
			t.Fatalf("Stop() = %v", err)
		}
	}

	if calls := account.called("StopTask"); len(calls) != 1 {
		t.Errorf("StopTask was called %d times, want 1", len(calls))
	}
	if running := account.runningTasks(); len(running) != 0 {
		t.Errorf("tasks %v are still running", running)
	}
}

func TestABastionTaskJoinsTheTiersClientsGroupSoTheDatabasesItForwardsToAdmitIt(t *testing.T) {
	t.Parallel()

	account := newAccount()
	clients := account.clients()
	spec := testSpec
	spec.Network.Clients = "sg-vpc-clients"
	reconciled, err := bastion.Reconcile(context.Background(), clients, spec)
	if err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}
	if _, err := reconciled.Run(context.Background(), clients); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	launched := account.tasks[account.runningTasks()[0]]
	if len(launched.groups) != 2 || launched.groups[0] != reconciled.SecurityGroup || launched.groups[1] != "sg-vpc-clients" {
		t.Errorf("the task joins %v, want the bastion's own group and the tier's clients group: a database admits the clients group alone", launched.groups)
	}
}
