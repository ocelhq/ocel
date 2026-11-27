package ports_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/variablestore"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
)

type policyStatement struct {
	Action    []string
	Condition map[string]map[string][]string
}

type deniedByPolicy struct {
	*fakeDynamo
	statements []policyStatement
}

func newDeniedByPolicy(t *testing.T, ddb *fakeDynamo, policy string) *deniedByPolicy {
	t.Helper()
	var doc struct{ Statement []policyStatement }
	if err := json.Unmarshal([]byte(policy), &doc); err != nil {
		t.Fatalf("policy is not valid JSON: %v", err)
	}
	return &deniedByPolicy{fakeDynamo: ddb, statements: doc.Statement}
}

func (d *deniedByPolicy) allow(action, partition string) error {
	for _, st := range d.statements {
		if !slices.Contains(st.Action, action) {
			continue
		}
		if slices.Contains(st.Condition["ForAllValues:StringEquals"]["dynamodb:LeadingKeys"], partition) {
			return nil
		}
	}
	return fmt.Errorf("AccessDeniedException: not authorized to perform %s on partition %s", action, partition)
}

func (d *deniedByPolicy) GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	if err := d.allow("dynamodb:GetItem", stringAttr(in.Key, "pk")); err != nil {
		return nil, err
	}
	return d.fakeDynamo.GetItem(ctx, in, opts...)
}

func (d *deniedByPolicy) Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	pk, _ := in.ExpressionAttributeValues[":pk"].(*ddbtypes.AttributeValueMemberS)
	if pk == nil {
		return nil, fmt.Errorf("query names no partition")
	}
	if err := d.allow("dynamodb:Query", pk.Value); err != nil {
		return nil, err
	}
	return d.fakeDynamo.Query(ctx, in, opts...)
}

func TestAnAppRoleReadsAValueItReferencesFromAnotherProject(t *testing.T) {
	ctx := context.Background()
	tier := environment.TierProduction
	ddb := newFakeDynamo()
	cipher := newCipherIgnoringKMSCalls()
	admin := variablestore.Store{KeyValues: awsports.KeyValues{Dynamo: ddb, Tables: awsports.Table(fakeVariablesTable)}, Cipher: cipher}

	baseline := variablestore.Scope{Project: "vt-baseline", Tier: tier}
	owned := variablestore.Scope{Project: "vt-owned", Tier: tier}
	password := variablestore.Cell{Key: "DB_PASSWORD"}
	ref := variablestore.Cell{Key: "REF_DB_PASSWORD"}
	if _, err := admin.Set(ctx, baseline, []variablestore.ValueWrite{{At: variablestore.Coordinate{Cell: password}, Plaintext: "hunter2"}}); err != nil {
		t.Fatalf("set %s: %v", password.Key, err)
	}
	if _, err := admin.SetReference(ctx, owned, variablestore.Coordinate{Cell: ref}, variablestore.Target{Project: baseline.Project, Cell: password}); err != nil {
		t.Fatalf("reference %s: %v", password.Key, err)
	}

	policy, err := variables.ReadPolicy(variables.Reader{
		TableARN:   "arn:aws:dynamodb:us-east-1:111122223333:table/" + fakeVariablesTable,
		Slug:       owned.Project,
		Tier:       string(tier),
		Referenced: []string{baseline.Project},
	})
	if err != nil {
		t.Fatalf("ReadPolicy: %v", err)
	}

	reader := variablestore.EnvironmentReader{
		KeyValues: awsports.KeyValues{Dynamo: newDeniedByPolicy(t, ddb, policy), Tables: awsports.Table(fakeVariablesTable)},
		Cipher:    cipher,
		Scope:     owned,
	}
	values, err := reader.Values(ctx, []variablestore.Cell{ref})
	if err != nil {
		t.Fatalf("the app role reading %s: %v\npolicy = %s", ref.Key, err, policy)
	}
	if got := values[ref.Key]; got != "hunter2" {
		t.Errorf("%s = %q, want the value %s/%s holds", ref.Key, got, baseline.Project, password.Key)
	}
}

func TestAnAppRoleReadsNoProjectItDoesNotReference(t *testing.T) {
	ctx := context.Background()
	tier := environment.TierProduction
	ddb := newFakeDynamo()
	cipher := newCipherIgnoringKMSCalls()
	admin := variablestore.Store{KeyValues: awsports.KeyValues{Dynamo: ddb, Tables: awsports.Table(fakeVariablesTable)}, Cipher: cipher}

	other := variablestore.Scope{Project: "billing", Tier: tier}
	if _, err := admin.Set(ctx, other, []variablestore.ValueWrite{{At: variablestore.Coordinate{Cell: variablestore.Cell{Key: "STRIPE_KEY"}}, Plaintext: "sk_live"}}); err != nil {
		t.Fatalf("set STRIPE_KEY: %v", err)
	}

	policy, err := variables.ReadPolicy(variables.Reader{
		TableARN: "arn:aws:dynamodb:us-east-1:111122223333:table/" + fakeVariablesTable,
		Slug:     "shop",
		Tier:     string(tier),
	})
	if err != nil {
		t.Fatalf("ReadPolicy: %v", err)
	}

	role := awsports.KeyValues{Dynamo: newDeniedByPolicy(t, ddb, policy), Tables: awsports.Table(fakeVariablesTable)}
	reader := variablestore.Store{KeyValues: role, Cipher: cipher}
	if _, err := reader.Get(ctx, other, variablestore.Coordinate{Cell: variablestore.Cell{Key: "STRIPE_KEY"}}, true); err == nil {
		t.Errorf("shop's role read billing/STRIPE_KEY, want it denied: shop references nothing in billing\npolicy = %s", policy)
	}
	if _, err := reader.Reveal(ctx, other, []variablestore.Coordinate{{Cell: variablestore.Cell{Key: "STRIPE_KEY"}}}); err == nil {
		t.Errorf("shop's role listed billing's values, want it denied\npolicy = %s", policy)
	}
}
