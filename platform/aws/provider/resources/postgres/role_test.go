package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rdsdata"
	rdsdatatypes "github.com/aws/aws-sdk-go-v2/service/rdsdata/types"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/postgres/signin"
)

type recordedStatements struct {
	executed []*rdsdata.ExecuteStatementInput
	failures []error
}

func (r *recordedStatements) ExecuteStatement(_ context.Context, in *rdsdata.ExecuteStatementInput, _ ...func(*rdsdata.Options)) (*rdsdata.ExecuteStatementOutput, error) {
	r.executed = append(r.executed, in)
	if len(r.failures) > 0 {
		err := r.failures[0]
		r.failures = r.failures[1:]
		return nil, err
	}
	return &rdsdata.ExecuteStatementOutput{}, nil
}

func testRole(statements StatementsAPI) *role {
	return &role{statements: statements, pause: func(context.Context, time.Duration) error { return nil }}
}

var testRoleArgs = roleArgs{
	Cluster:  "arn:aws:rds:us-east-1:111122223333:cluster:ocel-shop-prod-main",
	Secret:   "arn:aws:secretsmanager:us-east-1:111122223333:secret:rds!cluster-abc",
	Database: "ocel",
	Master:   "ocel",
	Role:     signin.AppRoleFor(environment.TierProduction),
}

func TestTheAppRoleOwnsTheDatabaseAndAuthenticatesByIAMAndTheMasterMayActAsIt(t *testing.T) {
	t.Parallel()

	statements := &recordedStatements{}
	if _, err := testRole(statements).Create(context.Background(), infer.CreateRequest[roleArgs]{Inputs: testRoleArgs}); err != nil {
		t.Fatalf("Create() = %v", err)
	}
	var sql []string
	for _, in := range statements.executed {
		if aws.ToString(in.ResourceArn) != testRoleArgs.Cluster || aws.ToString(in.SecretArn) != testRoleArgs.Secret || aws.ToString(in.Database) != "ocel" {
			t.Errorf("executed %+v, want it against the cluster, under its master secret, in its database", in)
		}
		sql = append(sql, aws.ToString(in.Sql))
	}
	want := []string{
		`CREATE ROLE "ocel_app_production" LOGIN`,
		`GRANT rds_iam TO "ocel_app_production"`,
		`GRANT "ocel_app_production" TO "ocel"`,
		`ALTER DATABASE "ocel" OWNER TO "ocel_app_production"`,
		`ALTER SCHEMA public OWNER TO "ocel_app_production"`,
	}
	if len(sql) != len(want) {
		t.Fatalf("executed %q, want %d statements", sql, len(want))
	}
	for i := range want {
		if !strings.Contains(sql[i], want[i]) {
			t.Errorf("statement %d = %q, want it to %s", i, sql[i], want[i])
		}
	}
	if !strings.Contains(sql[0], "IF NOT EXISTS") {
		t.Errorf("statement 0 = %q, want the role created only where it is missing, so a replay is harmless", sql[0])
	}
}

func TestAPreviewOfTheAppRoleRunsNoStatement(t *testing.T) {
	t.Parallel()

	statements := &recordedStatements{}
	if _, err := testRole(statements).Create(context.Background(), infer.CreateRequest[roleArgs]{Inputs: testRoleArgs, DryRun: true}); err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if len(statements.executed) != 0 {
		t.Errorf("a preview executed %d statements, want none", len(statements.executed))
	}
}

func TestAResumingDatabaseIsWaitedOutAndAnyOtherFailureEndsTheGrant(t *testing.T) {
	t.Parallel()

	resuming := &rdsdatatypes.DatabaseResumingException{Message: aws.String("resuming")}
	statements := &recordedStatements{failures: []error{resuming, resuming}}
	if _, err := testRole(statements).Create(context.Background(), infer.CreateRequest[roleArgs]{Inputs: testRoleArgs}); err != nil {
		t.Fatalf("Create() = %v, want a paused cluster waited for", err)
	}
	if len(statements.executed) != 7 {
		t.Errorf("executed %d calls, want the first statement retried twice then all five run", len(statements.executed))
	}

	refused := &recordedStatements{failures: []error{errors.New("access denied")}}
	if _, err := testRole(refused).Create(context.Background(), infer.CreateRequest[roleArgs]{Inputs: testRoleArgs}); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Errorf("Create() = %v, want the refusal surfaced", err)
	}
	if len(refused.executed) != 1 {
		t.Errorf("executed %d calls after a refusal, want one: only a resuming database is worth waiting for", len(refused.executed))
	}

	asleep := &recordedStatements{}
	for range resumeAttempts {
		asleep.failures = append(asleep.failures, resuming)
	}
	if _, err := testRole(asleep).Create(context.Background(), infer.CreateRequest[roleArgs]{Inputs: testRoleArgs}); err == nil {
		t.Error("Create() = nil after every attempt found the database resuming, want it to give up")
	}
	if len(asleep.executed) != resumeAttempts {
		t.Errorf("executed %d calls, want %d before giving up", len(asleep.executed), resumeAttempts)
	}
}
