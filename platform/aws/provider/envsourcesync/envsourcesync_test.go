package envsourcesync_test

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/envsourcesync"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const (
	testBoundary = "arn:aws:iam::" + testAccount + ":policy/ocel-app-boundary"
	testTable    = "ocel-bootstrap-VariablesTable-ABC"
	testKey      = "arn:aws:kms:us-east-1:" + testAccount + ":key/1234"
)

func spec(tier environment.Tier) envsourcesync.Spec {
	return envsourcesync.Spec{
		Namespace: "ocel",
		Tier:      tier,
		Region:    "us-east-1",
		Boundary:  testBoundary,
		RolePath:  "/ocel/" + string(tier) + "/app/",
		Table:     testTable,
		TableARN:  "arn:aws:dynamodb:us-east-1:" + testAccount + ":table/" + testTable,
		Key:       testKey,
	}
}

type statement struct {
	Effect    string
	Action    any
	Resource  any
	Principal map[string]any
	Condition map[string]map[string]string
}

func statements(t *testing.T, document string) []statement {
	t.Helper()
	var policy struct{ Statement []statement }
	if err := json.Unmarshal([]byte(document), &policy); err != nil {
		t.Fatalf("policy %q: %v", document, err)
	}
	return policy.Statement
}

func TestEnsureMakesAnEnvSourceSyncTheDeployCredentialMayMakeUnderTheAppBoundary(t *testing.T) {
	t.Parallel()
	account := newAccount()
	names := envsourcesync.NamesFor("ocel", environment.TierProduction)

	if _, err := envsourcesync.Ensure(context.Background(), account.clients(), spec(environment.TierProduction)); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}

	for _, name := range []string{names.Role, names.ScheduleRole} {
		r, ok := account.roles[name]
		if !ok {
			t.Fatalf("no role %s", name)
		}
		if r.path != "/ocel/production/app/" || r.boundary != testBoundary {
			t.Errorf("role %s is at %s under %q, want the app role path under the app boundary", name, r.path, r.boundary)
		}
		if r.tags["ocel:managed-by"] != "ocel" || r.tags["ocel:env-tier"] != "production" {
			t.Errorf("role %s tags = %v, want it tagged as ocel's, of production", name, r.tags)
		}
	}
	fn, ok := account.functions[names.Function]
	if !ok {
		t.Fatalf("no function %s", names.Function)
	}
	if !bytes.Equal(fn.code, payloads.EnvSourceSync().Bytes) {
		t.Error("the function runs another payload than the embedded env source sync")
	}
	if got := aws.ToString(fn.input.Role); got != "arn:aws:iam::"+testAccount+":role/ocel/production/app/"+names.Role {
		t.Errorf("the function runs as %s, want its own role", got)
	}
	want := map[string]string{"OCEL_VARIABLES_TABLE": testTable, "OCEL_VARIABLES_KEY": testKey, "OCEL_INFRA_TIER": "production"}
	if got := fn.input.Environment.Variables; !maps.Equal(got, want) {
		t.Errorf("environment = %v, want exactly %v", got, want)
	}
	if aws.ToInt32(fn.invokeRetry) != 0 || aws.ToInt32(fn.invokeAge) != 60 {
		t.Errorf("invoke config = %v retries within %vs, want none within a minute: the next minute is the retry", aws.ToInt32(fn.invokeRetry), aws.ToInt32(fn.invokeAge))
	}
	if aws.ToString(fn.input.LoggingConfig.LogGroup) != names.LogGroup || account.retention[names.LogGroup] != 14 {
		t.Errorf("logs to %s kept %d days, want %s kept 14", aws.ToString(fn.input.LoggingConfig.LogGroup), account.retention[names.LogGroup], names.LogGroup)
	}
	if !strings.HasPrefix(names.ScheduleGroup, "ocel-app-") {
		t.Errorf("schedule group %s is not one the deploy credential reaches by name", names.ScheduleGroup)
	}
	scheduled, ok := account.schedules[names.ScheduleGroup+"/"+names.Schedule]
	if !ok {
		t.Fatalf("no schedule %s in group %s", names.Schedule, names.ScheduleGroup)
	}
	target := scheduled.input.Target
	if aws.ToString(scheduled.input.ScheduleExpression) != "rate(1 minute)" || aws.ToString(target.Arn) != functionARN(names.Function) ||
		aws.ToString(target.RoleArn) != "arn:aws:iam::"+testAccount+":role/ocel/production/app/"+names.ScheduleRole ||
		aws.ToInt32(target.RetryPolicy.MaximumRetryAttempts) != 0 {
		t.Errorf("schedule = %s targeting %+v, want the function invoked once a minute through the schedule role, with no retry", aws.ToString(scheduled.input.ScheduleExpression), target)
	}
}

func TestTheEnvSourceSyncRolesReachOnlyWhatTheSyncNeeds(t *testing.T) {
	t.Parallel()
	account := newAccount()
	names := envsourcesync.NamesFor("ocel", environment.TierPreview)
	if _, err := envsourcesync.Ensure(context.Background(), account.clients(), spec(environment.TierPreview)); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}

	granted := map[string][]string{}
	for _, policy := range account.roles[names.Role].policies {
		for _, st := range statements(t, policy) {
			for _, resource := range anyStrings(st.Resource) {
				granted[resource] = append(granted[resource], anyStrings(st.Action)...)
			}
		}
	}
	table := "arn:aws:dynamodb:us-east-1:" + testAccount + ":table/" + testTable
	logs := "arn:aws:logs:us-east-1:" + testAccount + ":log-group:" + names.LogGroup + ":*"
	want := map[string][]string{
		table:   {"dynamodb:DeleteItem", "dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:Query"},
		testKey: {"kms:Decrypt", "kms:Encrypt"},
		logs:    {"logs:CreateLogStream", "logs:PutLogEvents"},
	}
	if len(granted) != len(want) {
		t.Errorf("the sync's role reaches %v, want exactly %v", granted, want)
	}
	for resource, actions := range want {
		if !slices.Equal(granted[resource], actions) {
			t.Errorf("the sync's role grants %v on %s, want %v", granted[resource], resource, actions)
		}
	}

	trust := statements(t, account.roles[names.ScheduleRole].trust)
	if len(trust) != 1 || trust[0].Principal["Service"] != "scheduler.amazonaws.com" ||
		trust[0].Condition["StringEquals"]["aws:SourceAccount"] != testAccount ||
		trust[0].Condition["ArnEquals"]["aws:SourceArn"] != "arn:aws:scheduler:us-east-1:"+testAccount+":schedule-group/"+names.ScheduleGroup {
		t.Errorf("the schedule role trusts %+v, want EventBridge Scheduler alone, from this tier's group in this account", trust)
	}
	for _, policy := range account.roles[names.ScheduleRole].policies {
		for _, st := range statements(t, policy) {
			if !slices.Equal(anyStrings(st.Action), []string{"lambda:InvokeFunction"}) || !slices.Equal(anyStrings(st.Resource), []string{functionARN(names.Function)}) {
				t.Errorf("the schedule role grants %v on %v, want only invoking the sync", st.Action, st.Resource)
			}
		}
	}
}

func anyStrings(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, each := range v {
			out = append(out, each.(string))
		}
		return out
	}
	return nil
}

func TestEnsuringASyncThatExistsUpdatesItInPlace(t *testing.T) {
	t.Parallel()
	account := newAccount()
	names := envsourcesync.NamesFor("ocel", environment.TierProduction)
	if _, err := envsourcesync.Ensure(context.Background(), account.clients(), spec(environment.TierProduction)); err != nil {
		t.Fatal(err)
	}
	account.functions[names.Function].code = []byte("an older build's sync")

	moved := spec(environment.TierProduction)
	moved.Key = "arn:aws:kms:us-east-1:" + testAccount + ":key/5678"
	if _, err := envsourcesync.Ensure(context.Background(), account.clients(), moved); err != nil {
		t.Fatalf("Ensure() over an existing sync = %v", err)
	}

	fn := account.functions[names.Function]
	if !bytes.Equal(fn.code, payloads.EnvSourceSync().Bytes) || fn.input.Environment.Variables["OCEL_VARIABLES_KEY"] != moved.Key {
		t.Errorf("the sync was not moved forward in place: key %s", fn.input.Environment.Variables["OCEL_VARIABLES_KEY"])
	}
}

func TestEnsureWaitsOutARoleLambdaCannotAssumeYet(t *testing.T) {
	t.Parallel()
	account := newAccount()
	account.unassumable = 2

	if _, err := envsourcesync.Ensure(context.Background(), account.clients(), spec(environment.TierProduction)); err != nil {
		t.Fatalf("Ensure() while the new role propagates = %v, want it waited out", err)
	}
	if _, ok := account.functions[envsourcesync.NamesFor("ocel", environment.TierProduction).Function]; !ok {
		t.Error("no function once the role could be assumed")
	}
}

func TestEnsureRefusesATierWithNoVariablesKey(t *testing.T) {
	t.Parallel()
	account := newAccount()
	keyless := spec(environment.TierProduction)
	keyless.Key = ""

	if _, err := envsourcesync.Ensure(context.Background(), account.clients(), keyless); err == nil {
		t.Fatal("Ensure() with no variables key = nil error, want it refused")
	}
	if len(account.calls) != 0 {
		t.Errorf("Ensure() with no key called %v", account.calls)
	}
}

func TestRemoveLeavesNothingOfATiersSyncAndLeavesTheOtherTiersAlone(t *testing.T) {
	t.Parallel()
	account := newAccount()
	ctx := context.Background()
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		if _, err := envsourcesync.Ensure(ctx, account.clients(), spec(tier)); err != nil {
			t.Fatal(err)
		}
	}

	if err := envsourcesync.Remove(ctx, account.clients(), "ocel", environment.TierProduction); err != nil {
		t.Fatalf("Remove() = %v", err)
	}

	preview := envsourcesync.NamesFor("ocel", environment.TierPreview)
	if len(account.roles) != 2 || len(account.functions) != 1 || len(account.logGroups) != 1 || len(account.groups) != 1 || len(account.schedules) != 1 {
		t.Errorf("Remove() left roles %v, functions %v, log groups %v, groups %v, schedules %v; want only preview's", account.roles, account.functions, account.logGroups, account.groups, account.schedules)
	}
	if _, ok := account.functions[preview.Function]; !ok {
		t.Error("Remove() of production took preview's sync")
	}
}

func TestRemoveOfATierWithNoSyncDeletesNothing(t *testing.T) {
	t.Parallel()
	account := newAccount()

	if err := envsourcesync.Remove(context.Background(), account.clients(), "ocel", environment.TierProduction); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
}
