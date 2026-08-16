package linkpublish

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/ocelhq/ocel/pkg/naming"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

type fakeCFN struct {
	outputs map[string]string
	err     error
}

func (f fakeCFN) DescribeStacks(context.Context, *cloudformation.DescribeStacksInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.outputs == nil {
		return &cloudformation.DescribeStacksOutput{}, nil
	}
	out := make([]cfntypes.Output, 0, len(f.outputs))
	for key, value := range f.outputs {
		out = append(out, cfntypes.Output{OutputKey: aws.String(key), OutputValue: aws.String(value)})
	}
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{Outputs: out}}}, nil
}

func bootstrapped() map[string]string {
	return map[string]string{
		"VarsTableName":       "ocel-vars",
		"VarsKeyArn":          "arn:aws:kms:us-east-1:111122223333:key/abcd",
		"InfrastructureClass": bootstrap.ClassProduction,
		"BootstrapVersion":    "10",
		"StateBucketName":     "ocel-state",
		"ArtifactBucketName":  "ocel-artifacts",
		"AssetBucketName":     "ocel-assets",
		"StateTableName":      "ocel-state",
		"ImageOptimizerUrl":   "https://optimizer.example",
		"RevalidateQueueUrl":  "https://queue.example",
	}
}

func TestSubstrateNamesItsAbsence(t *testing.T) {
	t.Run("no bootstrap stack at all", func(t *testing.T) {
		_, err := Substrate(context.Background(), fakeCFN{}, bootstrap.ClassProduction)

		if !errors.Is(err, ErrNoSubstrate) {
			t.Fatalf("Substrate = %v, want ErrNoSubstrate", err)
		}
		if !strings.Contains(err.Error(), "ocel bootstrap") {
			t.Errorf("Substrate said %q, which never names what a user runs to fix it", err)
		}
	})

	t.Run("a bootstrap without a variable store", func(t *testing.T) {
		outputs := bootstrapped()
		delete(outputs, "VarsTableName")

		_, err := Substrate(context.Background(), fakeCFN{outputs: outputs}, bootstrap.ClassProduction)

		if !errors.Is(err, ErrNoSubstrate) {
			t.Fatalf("Substrate = %v, want ErrNoSubstrate", err)
		}
	})

	t.Run("a bootstrap that is there", func(t *testing.T) {
		found, err := Substrate(context.Background(), fakeCFN{outputs: bootstrapped()}, bootstrap.ClassProduction)
		if err != nil {
			t.Fatalf("Substrate: %v", err)
		}
		if found.VarsTable != "ocel-vars" || found.VarsKeyARN == "" {
			t.Fatalf("Substrate found %+v, want the variable store's table and key", found)
		}
	})
}

const ordersRecord = `{"name":"orders","source":"sst","postgres":{"host":"h","port":5432,"database":"d","username":"u","password":"p"}}`

func TestRequestValidates(t *testing.T) {
	valid := Request{
		Project:   "shop",
		Publisher: "sst",
		Class:     bootstrap.ClassProduction,
		Records:   []json.RawMessage{json.RawMessage(ordersRecord)},
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	for name, mutate := range map[string]func(*Request){
		"no project":        func(r *Request) { r.Project = "" },
		"no publisher":      func(r *Request) { r.Publisher = "" },
		"unknown class":     func(r *Request) { r.Class = "staging" },
		"class-wide marker": func(r *Request) { r.Environment = "*" },
	} {
		t.Run(name, func(t *testing.T) {
			req := Request{Project: valid.Project, Publisher: valid.Publisher, Class: valid.Class, Records: []json.RawMessage{valid.Records[0]}}
			mutate(&req)
			if err := req.validate(); err == nil {
				t.Fatalf("validate accepted %s", name)
			}
		})
	}
}

func TestRecordsRefuseWhatTheStoreCannotBind(t *testing.T) {
	for name, tc := range map[string]struct {
		record string
		want   string
	}{
		"unnamed record":        {`{"source":"sst","postgres":{"host":"h"}}`, "carries no name"},
		"untyped record":        {`{"name":"orders","source":"sst"}`, "carries no properties"},
		"not a link at all":     {`{"name":"orders","type":"sst:aws.Postgres","properties":{"host":"h"}}`, "not a links.v1.Link"},
		"grant over a wildcard": {`{"name":"orders","source":"sst","postgres":{"host":"h"},"grants":[{"actions":["rds-db:connect"],"resources":["*"]}]}`, "nothing else"},
	} {
		t.Run(name, func(t *testing.T) {
			req := Request{Records: []json.RawMessage{json.RawMessage(tc.record)}}
			_, err := req.records()
			if err == nil {
				t.Fatalf("records() accepted %s", name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("records() = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

func TestDestroyTreatsAnAbsentSubstrateAsAlreadyPruned(t *testing.T) {
	req := Request{Project: "shop", Publisher: "sst:OcelLinks", Class: bootstrap.ClassProduction}

	if _, err := Destroy(context.Background(), Clients{CFN: fakeCFN{}}, req); err != nil {
		t.Fatalf("Destroy = %v, want a stack whose store is already gone to remove cleanly", err)
	}

	if _, err := Apply(context.Background(), Clients{CFN: fakeCFN{}}, req); !errors.Is(err, ErrNoSubstrate) {
		t.Fatalf("Apply = %v, want ErrNoSubstrate: publishing still needs somewhere to land", err)
	}
}

func TestRequestRefusesAnEnvironmentOutsidePreview(t *testing.T) {
	req := Request{
		Project:     "shop",
		Publisher:   "sst:OcelLinks",
		Class:       bootstrap.ClassProduction,
		Environment: "pr-9",
	}
	if err := req.validate(); err == nil {
		t.Fatal("validate accepted a named environment in the production class; an ocel coordinate names a preview environment or none")
	}

	req.Class = bootstrap.ClassPreview
	if err := req.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestRecordsDecodeToWhatTheStoreTakes(t *testing.T) {
	req := Request{
		Project:   "shop",
		Publisher: "sst",
		Class:     bootstrap.ClassProduction,
		Records: []json.RawMessage{json.RawMessage(`{
			"name": "orders",
			"source": "sst",
			"postgres": {"host": "h", "port": 5432, "database": "d", "username": "u", "password": "p"},
			"grants": [{
				"actions": ["rds-db:connect"],
				"resources": ["arn:aws:rds-db:us-east-1:1234:dbuser:db-ORDERS/app"],
				"label": "connect"
			}]
		}`)},
	}

	got, err := req.records()
	if err != nil {
		t.Fatalf("records(): %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("records() returned %d, want one per consumable resource", len(got))
	}
	if got[0].GetName() != "orders" || got[0].GetSource() != "sst" || naming.LinkTypeOf(got[0]) != linksv1.LinkType_LINK_TYPE_POSTGRES {
		t.Errorf("records()[0] = %+v", got[0])
	}
	if got[0].GetPostgres().GetPort() != 5432 || got[0].GetPostgres().GetHost() != "h" {
		t.Errorf("records()[0] lost a property: %+v", got[0].GetPostgres())
	}
	if len(got[0].GetGrants()) != 1 || got[0].GetGrants()[0].GetLabel() != "connect" {
		t.Errorf("records()[0] grants = %+v", got[0].GetGrants())
	}
}

func TestRequestDecodesTheWireShape(t *testing.T) {
	var req Request
	if err := json.Unmarshal([]byte(`{
		"project": "shop",
		"publisher": "sst",
		"class": "preview",
		"environment": "pr-9",
		"region": "us-east-1",
		"records": [{
			"name": "orders",
			"source": "sst",
			"postgres": {"host": "h", "port": 5432, "database": "d", "username": "u", "password": "p"},
			"grants": [{"actions": ["rds-db:connect"], "resources": ["arn:x"], "label": "connect"}]
		}]
	}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := req.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if _, err := req.records(); err != nil {
		t.Fatalf("records(): %v", err)
	}
	if req.Environment != "pr-9" || req.Region != "us-east-1" {
		t.Fatalf("decoded %+v", req)
	}
}
