package connector

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

type accountConcurrency int32

func (c accountConcurrency) GetAccountSettings(context.Context, *lambda.GetAccountSettingsInput, ...func(*lambda.Options)) (*lambda.GetAccountSettingsOutput, error) {
	return &lambda.GetAccountSettingsOutput{AccountLimit: &lambdatypes.AccountLimit{UnreservedConcurrentExecutions: aws.Int32(int32(c))}}, nil
}

func TestTheConnectorReservesItsConcurrencyOnlyWhereTheAccountKeepsItsUnreservedMinimum(t *testing.T) {
	t.Parallel()

	for unreserved, want := range map[accountConcurrency]int32{10: 0, 109: 0, 110: reservedConcurrency, 1000: reservedConcurrency} {
		reserved, err := reservationFor(context.Background(), unreserved)
		if err != nil || reserved != want {
			t.Errorf("reservationFor(%d unreserved) = %d, %v, want %d: Lambda refuses a reservation that leaves fewer than %d unreserved", unreserved, reserved, err, want, unreservedMinimum)
		}
	}
	at := payloads.At("staged-bucket", codePayload, payloads.Of([]byte("connector")))
	rendered, err := templateFor(defaultNamespace, at, Bundle{Version: "0.9.9", PublicKey: publicKey, Config: []byte(`{}`)}, sealingKeys, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered, "ReservedConcurrentExecutions") {
		t.Error("the template reserves concurrency in an account that has none to spare, so the function fails to create")
	}
}

type stagedObjects struct {
	payloads.ObjectStore
	keys   []string
	bodies []string
}

func (s *stagedObjects) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	body, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	s.keys = append(s.keys, aws.ToString(in.Bucket)+"/"+aws.ToString(in.Key))
	s.bodies = append(s.bodies, string(body))
	return &s3.PutObjectOutput{}, nil
}

func TestAConnectorTemplateOverWhatCloudFormationTakesInlineIsStagedAndWrittenByURL(t *testing.T) {
	t.Parallel()

	statements := make([]iam.Statement, 0, 400)
	for range 400 {
		statements = append(statements, iam.Statement{Actions: []string{"logs:FilterLogEvents"}, Resources: []string{"arn:aws:logs:*:*:log-group:/aws/lambda/ocel-app-shop-prod-web-*"}})
	}
	policies, err := iam.Split("connector", func(i int) string { return "ocel-connector-" + string(rune('0'+i)) }, statements)
	if err != nil {
		t.Fatal(err)
	}
	at := payloads.At("staged-bucket", codePayload, payloads.Of([]byte("connector")))
	rendered, err := templateFor(defaultNamespace, at, Bundle{Version: "0.9.9", PublicKey: publicKey, Config: []byte(`{}`), Policies: policies}, sealingKeys, reservedConcurrency)
	if err != nil {
		t.Fatal(err)
	}
	if len(rendered) <= cfn.TemplateBodyBytes {
		t.Fatalf("the template is %d bytes, want one over the %d CloudFormation takes inline to stage", len(rendered), cfn.TemplateBodyBytes)
	}
	if !json.Valid([]byte(rendered)) {
		t.Fatal("the template is not JSON")
	}

	objects := &stagedObjects{}
	source, err := stagedTemplate(context.Background(), APIs{Objects: objects, Region: "us-west-2"}, "staged-bucket", rendered)
	if err != nil {
		t.Fatalf("stagedTemplate() = %v", err)
	}
	if source.Body != "" || !strings.HasPrefix(source.URL, "https://staged-bucket.s3.us-west-2.amazonaws.com/templates/") {
		t.Errorf("stagedTemplate() = %+v, want the template written by its staged URL", source)
	}
	if len(objects.bodies) != 1 || objects.bodies[0] != rendered {
		t.Errorf("staged %v, want the rendered template once", objects.keys)
	}

	small, err := stagedTemplate(context.Background(), APIs{Objects: objects, Region: "us-west-2"}, "staged-bucket", "{}")
	if err != nil || small.Body != "{}" || small.URL != "" || len(objects.keys) != 1 {
		t.Errorf("stagedTemplate(small) = %+v, %v, want it inline and nothing staged", small, err)
	}
}

func TestTheInstallAcknowledgesTheNamedPoliciesItsTemplateCreates(t *testing.T) {
	t.Parallel()

	at := payloads.At("staged-bucket", codePayload, payloads.Of([]byte("connector")))
	policies := []iam.ManagedPolicy{{PolicyName: "ocel-connector-1", PolicyDocument: iam.Document([]iam.Statement{{Actions: []string{"logs:FilterLogEvents"}, Resources: []string{"*"}}})}}
	rendered, err := templateFor(defaultNamespace, at, Bundle{Version: "0.9.9", PublicKey: publicKey, Config: []byte(`{}`), Policies: policies}, sealingKeys, reservedConcurrency)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered, `"ManagedPolicyName"`) && !slices.Contains(installCapabilities, cfntypes.CapabilityCapabilityNamedIam) {
		t.Errorf("the install acknowledges %v, and CloudFormation refuses a template that names its IAM resources without CAPABILITY_NAMED_IAM", installCapabilities)
	}
}
