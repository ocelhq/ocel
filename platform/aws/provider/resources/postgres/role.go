package postgres

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rdsdata"
	rdsdatatypes "github.com/aws/aws-sdk-go-v2/service/rdsdata/types"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
)

const (
	appRolePrefix = "ocel_app_"
	RoleToken     = "ocel:index:PostgresRole"

	resumeAttempts  = 10
	resumeBaseDelay = 2 * time.Second
	resumeMaxDelay  = 20 * time.Second
)

func AppRoleFor(tier environment.Tier) string { return appRolePrefix + string(tier) }

type StatementsAPI interface {
	ExecuteStatement(ctx context.Context, in *rdsdata.ExecuteStatementInput, optFns ...func(*rdsdata.Options)) (*rdsdata.ExecuteStatementOutput, error)
}

type roleArgs struct {
	Cluster  string `pulumi:"cluster"`
	Secret   string `pulumi:"secret"`
	Database string `pulumi:"database"`
	Master   string `pulumi:"master"`
	Role     string `pulumi:"role"`
}

type roleOutputs struct {
	roleArgs
}

type role struct {
	statements StatementsAPI
	pause      func(ctx context.Context, d time.Duration) error
}

func RoleResource(statements StatementsAPI) infer.InferredResource {
	return infer.Resource(&role{statements: statements, pause: sleep})
}

func (*role) Annotate(a infer.Annotator) { a.SetToken("index", "PostgresRole") }

func (r *role) Create(ctx context.Context, req infer.CreateRequest[roleArgs]) (infer.CreateResponse[roleOutputs], error) {
	id := req.Inputs.Cluster + "/" + req.Inputs.Role
	if req.DryRun {
		return infer.CreateResponse[roleOutputs]{ID: id, Output: roleOutputs{roleArgs: req.Inputs}}, nil
	}
	if err := r.grant(ctx, req.Inputs); err != nil {
		return infer.CreateResponse[roleOutputs]{}, err
	}
	return infer.CreateResponse[roleOutputs]{ID: id, Output: roleOutputs{roleArgs: req.Inputs}}, nil
}

func (r *role) grant(ctx context.Context, args roleArgs) error {
	if r.statements == nil {
		return errors.New("this provider holds no RDS Data API client to create the app's database role with")
	}
	for _, statement := range roleStatements(args) {
		if err := r.execute(ctx, args, statement); err != nil {
			return fmt.Errorf("give %s the %s database: %w", args.Role, args.Database, err)
		}
	}
	return nil
}

func (r *role) execute(ctx context.Context, args roleArgs, statement string) error {
	delay := resumeBaseDelay
	for attempt := 1; ; attempt++ {
		_, err := r.statements.ExecuteStatement(ctx, &rdsdata.ExecuteStatementInput{
			ResourceArn: aws.String(args.Cluster),
			SecretArn:   aws.String(args.Secret),
			Database:    aws.String(args.Database),
			Sql:         aws.String(statement),
		})
		var resuming *rdsdatatypes.DatabaseResumingException
		if err == nil || !errors.As(err, &resuming) || attempt == resumeAttempts {
			return err
		}
		if err := r.pause(ctx, delay/2+rand.N(delay/2+1)); err != nil {
			return err
		}
		delay = min(2*delay, resumeMaxDelay)
	}
}

func roleStatements(args roleArgs) []string {
	role, master, database := quoteIdentifier(args.Role), quoteIdentifier(args.Master), quoteIdentifier(args.Database)
	return []string{
		"DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = " + quoteLiteral(args.Role) + ") THEN CREATE ROLE " + role + " LOGIN; END IF; END $$",
		"GRANT rds_iam TO " + role,
		"GRANT " + role + " TO " + master,
		"ALTER DATABASE " + database + " OWNER TO " + role,
		"ALTER SCHEMA public OWNER TO " + role,
	}
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type appRoleState struct {
	pulumi.CustomResourceState
}
