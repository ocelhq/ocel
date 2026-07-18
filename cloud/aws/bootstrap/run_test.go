package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	smithy "github.com/aws/smithy-go"
	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/cloud/edge"
)

// fakeCFN is a CloudFormation account holding one stack per name, recording the
// template body each upsert submitted so a test can assert what the account was
// actually asked to provision. It reports a missing stack the way CloudFormation
// does — a ValidationError whose message says it does not exist — and settles
// every create/update immediately so the waiters return on their first describe.
type fakeCFN struct {
	templates map[string]string
	statuses  map[string]cfntypes.StackStatus
	creates   int
	updates   int
}

func newFakeCFN() *fakeCFN {
	return &fakeCFN{templates: map[string]string{}, statuses: map[string]cfntypes.StackStatus{}}
}

// validationError is CloudFormation's untyped ValidationError, which is how it
// reports both a missing stack and a no-op update.
type validationError struct{ msg string }

func (e validationError) Error() string                 { return e.msg }
func (e validationError) ErrorCode() string             { return "ValidationError" }
func (e validationError) ErrorMessage() string          { return e.msg }
func (e validationError) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

func (f *fakeCFN) DescribeStacks(_ context.Context, in *cloudformation.DescribeStacksInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	name := aws.ToString(in.StackName)
	if _, ok := f.templates[name]; !ok {
		return nil, validationError{msg: "Stack with id " + name + " does not exist"}
	}
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{
		StackName:   aws.String(name),
		StackStatus: f.statuses[name],
	}}}, nil
}

func (f *fakeCFN) CreateStack(_ context.Context, in *cloudformation.CreateStackInput, _ ...func(*cloudformation.Options)) (*cloudformation.CreateStackOutput, error) {
	f.creates++
	f.templates[aws.ToString(in.StackName)] = aws.ToString(in.TemplateBody)
	f.statuses[aws.ToString(in.StackName)] = cfntypes.StackStatusCreateComplete
	return &cloudformation.CreateStackOutput{}, nil
}

func (f *fakeCFN) UpdateStack(_ context.Context, in *cloudformation.UpdateStackInput, _ ...func(*cloudformation.Options)) (*cloudformation.UpdateStackOutput, error) {
	f.updates++
	f.templates[aws.ToString(in.StackName)] = aws.ToString(in.TemplateBody)
	f.statuses[aws.ToString(in.StackName)] = cfntypes.StackStatusUpdateComplete
	return &cloudformation.UpdateStackOutput{}, nil
}

// fakeEdge is an edge.Provider that reports whatever bootstrap output a test
// chooses. It records every call so a test can prove the edge was bootstrapped
// and that nothing called back into it.
type fakeEdge struct {
	out        edge.BootstrapOutput
	err        error
	bootstraps int
}

func (f *fakeEdge) Kind() edge.Kind { return "fake" }

func (f *fakeEdge) Bootstrap(context.Context) (edge.BootstrapOutput, error) {
	f.bootstraps++
	return f.out, f.err
}

func (f *fakeEdge) DeployApp(context.Context, edge.AppDeployment) (edge.AppResult, error) {
	return edge.AppResult{}, errors.New("DeployApp must not run during bootstrap")
}

// hasEdgeUser reports whether the template the account was provisioned with
// contains the edge reader IAM user at all.
func hasEdgeUser(t *testing.T, template string) bool {
	t.Helper()
	var tmpl struct {
		Resources map[string]struct {
			Type string `yaml:"Type"`
		} `yaml:"Resources"`
	}
	if err := yaml.Unmarshal([]byte(template), &tmpl); err != nil {
		t.Fatalf("provisioned template is not valid YAML: %v", err)
	}
	for _, r := range tmpl.Resources {
		if r.Type == "AWS::IAM::User" {
			return true
		}
	}
	return false
}

// TestRun_ExternalTrustProvisionsEdgeReader proves an edge outside the trust
// boundary gets what it needs to sign its own reads: the edge reader IAM user in
// the account's template, and a static access key stored for the deploy path.
func TestRun_ExternalTrustProvisionsEdgeReader(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}

	if err := Run(context.Background(), cfn, ssmc, iamc, ed, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ed.bootstraps != 1 {
		t.Errorf("edge bootstrapped %d times, want exactly 1", ed.bootstraps)
	}
	if !hasEdgeUser(t, cfn.templates[StackName]) {
		t.Error("external trust did not provision the edge reader IAM user")
	}
	if len(iamc.created) != 1 || iamc.created[0] != EdgeUserName {
		t.Errorf("minted keys for %v, want [%s]", iamc.created, EdgeUserName)
	}
	if _, ok := ssmc.params[EdgeCredentialsParamName]; !ok {
		t.Errorf("no static key stored at %s", EdgeCredentialsParamName)
	}
}

// TestRun_InternalTrustLeavesNoCredential is the crux of the trust posture: an
// edge inside the provider's boundary reads under the provider's own identity,
// so bootstrap must leave neither an IAM user nor any long-lived key behind for
// an attacker to find.
func TestRun_InternalTrustLeavesNoCredential(t *testing.T) {
	for _, tc := range []struct {
		name      string
		run       func(context.Context, CFNAPI, SSMAPI, IAMAPI, edge.Provider, func(string), func(string)) error
		stackName string
		credParam string
	}{
		{"production", Run, StackName, EdgeCredentialsParamName},
		{"preview", RunPreview, PreviewStackName, EdgeCredentialsPreviewParamName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
			ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustInternal}}

			if err := tc.run(context.Background(), cfn, ssmc, iamc, ed, nil, nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			template := cfn.templates[tc.stackName]
			if template == "" {
				t.Fatalf("no template was provisioned for %s", tc.stackName)
			}
			if hasEdgeUser(t, template) {
				t.Errorf("internal trust provisioned an IAM user:\n%s", template)
			}
			if strings.Contains(template, EdgeUserName) || strings.Contains(template, EdgePreviewUserName) {
				t.Errorf("internal trust template still names an edge reader:\n%s", template)
			}
			if len(iamc.created) != 0 {
				t.Errorf("internal trust minted static keys for %v", iamc.created)
			}
			if _, ok := ssmc.params[tc.credParam]; ok {
				t.Errorf("internal trust stored a static key at %s", tc.credParam)
			}
		})
	}
}

// TestRun_PreviewTakesEdgeFirstPath proves the preview substrate reaches the same
// account state through the same edge-first path, under its own identities.
func TestRun_PreviewTakesEdgeFirstPath(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}

	if err := RunPreview(context.Background(), cfn, ssmc, iamc, ed, nil, nil); err != nil {
		t.Fatalf("RunPreview: %v", err)
	}
	if ed.bootstraps != 1 {
		t.Errorf("edge bootstrapped %d times, want exactly 1", ed.bootstraps)
	}
	if !hasEdgeUser(t, cfn.templates[PreviewStackName]) {
		t.Error("preview external trust did not provision the edge reader IAM user")
	}
	if len(iamc.created) != 1 || iamc.created[0] != EdgePreviewUserName {
		t.Errorf("minted keys for %v, want [%s]", iamc.created, EdgePreviewUserName)
	}
	if _, ok := ssmc.params[EdgeCredentialsPreviewParamName]; !ok {
		t.Errorf("no static key stored at %s", EdgeCredentialsPreviewParamName)
	}
}

// TestRun_PersistsEdgeValues proves the edge's own outputs survive bootstrap and
// come back byte-for-byte at deploy time — the edge reads back what it
// provisioned without re-querying its API.
func TestRun_PersistsEdgeValues(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	values := map[string]string{"bucketName": "edge-cache-7f3", "namespaceId": "ns-42"}
	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal, Values: values}}

	if err := Run(context.Background(), cfn, ssmc, iamc, ed, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := ReadEdgeValues(context.Background(), ssmc, ClassProduction)
	if err != nil {
		t.Fatalf("ReadEdgeValues: %v", err)
	}
	if len(got) != len(values) {
		t.Fatalf("read back %v, want %v", got, values)
	}
	for k, v := range values {
		if got[k] != v {
			t.Errorf("value %q = %q, want %q", k, got[k], v)
		}
	}
}

// TestRun_NoEdgeValuesStoresNothing proves an edge that provisioned nothing
// leaves no parameter behind, so an account fronted by such an edge looks exactly
// as it did before edge values existed.
func TestRun_NoEdgeValuesStoresNothing(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}

	if err := Run(context.Background(), cfn, ssmc, iamc, ed, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := ssmc.params[EdgeValuesParamName]; ok {
		t.Errorf("stored an edge values parameter for an edge that reported none")
	}
	got, err := ReadEdgeValues(context.Background(), ssmc, ClassProduction)
	if err != nil {
		t.Fatalf("ReadEdgeValues on an absent parameter: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ReadEdgeValues = %v, want empty", got)
	}
}

// TestRun_IgnoresUnrecognisedOffer proves a newer edge offering a resource this
// provider has never heard of degrades rather than breaking: the offer is
// ignored and bootstrap still completes.
func TestRun_IgnoresUnrecognisedOffer(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	ed := &fakeEdge{out: edge.BootstrapOutput{
		Trust: edge.TrustExternal,
		Offers: []edge.Offer{
			{Kind: "something-invented-later", Values: map[string]string{"id": "x"}},
			{Kind: edge.OfferCacheStore, Values: map[string]string{"bucket": "b"}},
		},
	}}

	if err := Run(context.Background(), cfn, ssmc, iamc, ed, nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !hasEdgeUser(t, cfn.templates[StackName]) {
		t.Error("an unrecognised offer changed what was provisioned")
	}
	if len(iamc.created) != 1 {
		t.Errorf("an unrecognised offer changed what was minted: %v", iamc.created)
	}
}

// TestRun_Idempotent proves a second bootstrap against a live substrate mints no
// second key — the AWS 2-key cap makes a duplicate mint a wedge, not a waste.
func TestRun_Idempotent(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	ed := &fakeEdge{out: edge.BootstrapOutput{
		Trust:  edge.TrustExternal,
		Values: map[string]string{"namespaceId": "ns-42"},
	}}

	for i := range 2 {
		if err := Run(context.Background(), cfn, ssmc, iamc, ed, nil, nil); err != nil {
			t.Fatalf("Run %d: %v", i+1, err)
		}
	}
	if len(iamc.created) != 1 {
		t.Errorf("minted %d keys across two bootstraps, want 1: %v", len(iamc.created), iamc.created)
	}
	var creds EdgeCredentials
	if err := json.Unmarshal([]byte(ssmc.params[EdgeCredentialsParamName]), &creds); err != nil {
		t.Fatalf("stored credentials are not readable after a re-run: %v", err)
	}
	if creds.AccessKeyID != "AKIAEDGE" {
		t.Errorf("stored key = %q, want the first minted key", creds.AccessKeyID)
	}
	if cfn.creates != 1 || cfn.updates != 1 {
		t.Errorf("stack was created %d and updated %d times, want 1 and 1", cfn.creates, cfn.updates)
	}
}

// TestRun_EdgeBootstrapFailureStopsProvisioning proves the edge runs first: when
// it fails, nothing of the provider's own has been created yet.
func TestRun_EdgeBootstrapFailureStopsProvisioning(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	ed := &fakeEdge{err: errors.New("edge API unreachable")}

	err := Run(context.Background(), cfn, ssmc, iamc, ed, nil, nil)
	if err == nil {
		t.Fatal("expected Run to fail when the edge bootstrap fails")
	}
	if len(cfn.templates) != 0 {
		t.Errorf("provisioned %d stacks despite a failed edge bootstrap", len(cfn.templates))
	}
	if len(iamc.created) != 0 || len(ssmc.params) != 0 {
		t.Errorf("minted %v / stored %d parameters despite a failed edge bootstrap", iamc.created, len(ssmc.params))
	}
}
