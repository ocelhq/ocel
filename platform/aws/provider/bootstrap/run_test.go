package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	smithy "github.com/aws/smithy-go"
	"gopkg.in/yaml.v3"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type fakeCFN struct {
	templates map[string]string
	statuses  map[string]cfntypes.StackStatus
	outputs   map[string]string
	creates   int
	updates   int
	noops     int
}

func newFakeCFN() *fakeCFN {
	return &fakeCFN{
		templates: map[string]string{},
		statuses:  map[string]cfntypes.StackStatus{},
		outputs:   map[string]string{outputArtifactBucket: "ocel-artifacts-test"},
	}
}

var templateVersionRE = regexp.MustCompile(`(?s)` + outputVersion + `:.*?Value: '(\d+)'`)

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
	var outputs []cfntypes.Output
	for k, v := range f.outputs {
		outputs = append(outputs, cfntypes.Output{OutputKey: aws.String(k), OutputValue: aws.String(v)})
	}
	if _, ok := f.outputs[outputVersion]; !ok {
		if m := templateVersionRE.FindStringSubmatch(f.templates[name]); m != nil {
			outputs = append(outputs, cfntypes.Output{OutputKey: aws.String(outputVersion), OutputValue: aws.String(m[1])})
		}
	}
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{
		StackName:   aws.String(name),
		StackStatus: f.statuses[name],
		Outputs:     outputs,
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
	name, body := aws.ToString(in.StackName), aws.ToString(in.TemplateBody)
	if f.templates[name] == body {
		f.noops++
		return nil, validationError{msg: "No updates are to be performed."}
	}
	f.templates[name] = body
	f.statuses[name] = cfntypes.StackStatusUpdateComplete
	return &cloudformation.UpdateStackOutput{}, nil
}

func (*fakeEdge) AssembleApp(edge.WorkerSource, edge.Resolver) (edge.Worker, error) {
	return edge.Worker{}, errors.New("bootstrap never assembles an app worker")
}

type fakeEdge struct {
	out        edge.BootstrapOutput
	err        error
	bootstraps int
	class      edge.Class
}

func (f *fakeEdge) Kind() edge.Kind { return "fake" }

func (f *fakeEdge) Bootstrap(_ context.Context, class edge.Class) (edge.BootstrapOutput, error) {
	f.bootstraps++
	f.class = class
	return f.out, f.err
}

func (f *fakeEdge) DeployApp(context.Context, edge.AppDeployment) (edge.AppResult, error) {
	return edge.AppResult{}, errors.New("DeployApp must not run during bootstrap")
}

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

func TestRun_ExternalTrustProvisionsEdgeReader(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}

	if err := Run(context.Background(), cfn, ssmc, iamc, ed, preloadedArtifact(), nil, nil); err != nil {
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

func TestRun_BootstrapsTheEdgeForItsOwnSubstrateClass(t *testing.T) {
	for _, tc := range []struct {
		run  func(context.Context, CFNAPI, SSMAPI, IAMAPI, edge.Provider, Artifacts, func(string), func(string)) error
		want edge.Class
	}{
		{Run, edge.ClassProduction},
		{RunPreview, edge.ClassPreview},
	} {
		t.Run(string(tc.want), func(t *testing.T) {
			ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}
			if err := tc.run(context.Background(), newFakeCFN(), newFakeSSM(), &fakeIAM{}, ed, preloadedArtifact(), nil, nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			if ed.class != tc.want {
				t.Errorf("edge bootstrapped for class %q, want %q", ed.class, tc.want)
			}
		})
	}
}

func TestRun_InternalTrustLeavesNoCredential(t *testing.T) {
	for _, tc := range []struct {
		name      string
		run       func(context.Context, CFNAPI, SSMAPI, IAMAPI, edge.Provider, Artifacts, func(string), func(string)) error
		stackName string
		credParam string
	}{
		{"production", Run, StackName, EdgeCredentialsParamName},
		{"preview", RunPreview, PreviewStackName, EdgeCredentialsPreviewParamName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
			ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustInternal}}

			if err := tc.run(context.Background(), cfn, ssmc, iamc, ed, preloadedArtifact(), nil, nil); err != nil {
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

func TestRun_PreviewTakesEdgeFirstPath(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}

	if err := RunPreview(context.Background(), cfn, ssmc, iamc, ed, preloadedArtifact(), nil, nil); err != nil {
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

func TestRun_PersistsEdgeValues(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	values := map[string]string{"bucketName": "edge-cache-7f3", "namespaceId": "ns-42"}
	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal, Values: values}}

	if err := Run(context.Background(), cfn, ssmc, iamc, ed, preloadedArtifact(), nil, nil); err != nil {
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

func TestRun_NoEdgeValuesStoresNothing(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}

	if err := Run(context.Background(), cfn, ssmc, iamc, ed, preloadedArtifact(), nil, nil); err != nil {
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

func TestRun_IgnoresUnrecognisedOffer(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	ed := &fakeEdge{out: edge.BootstrapOutput{
		Trust: edge.TrustExternal,
		Offers: []edge.Offer{
			{Kind: "something-invented-later", Values: map[string]string{"id": "x"}},
			{Kind: edge.OfferCacheStore, Values: offeredStore()},
		},
	}}

	if err := Run(context.Background(), cfn, ssmc, iamc, ed, preloadedArtifact(), nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !hasEdgeUser(t, cfn.templates[StackName]) {
		t.Error("an unrecognised offer changed what was provisioned")
	}
	if len(iamc.created) != 1 {
		t.Errorf("an unrecognised offer changed what was minted: %v", iamc.created)
	}
	if _, ok := ssmc.params[CacheStoreParamName]; !ok {
		t.Errorf("the recognised offer alongside it was not adopted")
	}
}

func TestRun_NoOffersStoresNoCacheStore(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}

	if err := Run(context.Background(), cfn, ssmc, iamc, ed, preloadedArtifact(), nil, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := ssmc.params[CacheStoreParamName]; ok {
		t.Errorf("stored a cache store for an edge that offered none")
	}
}

func TestRun_AdoptsCacheStorePerClass(t *testing.T) {
	for _, tc := range []struct {
		name  string
		run   func(context.Context, CFNAPI, SSMAPI, IAMAPI, edge.Provider, Artifacts, func(string), func(string)) error
		class string
		param string
	}{
		{"production", Run, ClassProduction, CacheStoreParamName},
		{"preview", RunPreview, ClassPreview, CacheStorePreviewParamName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ssmc := newFakeSSM()
			ed := &fakeEdge{out: edge.BootstrapOutput{
				Trust:  edge.TrustExternal,
				Offers: []edge.Offer{{Kind: edge.OfferCacheStore, Values: offeredStore()}},
			}}

			if err := tc.run(context.Background(), newFakeCFN(), ssmc, &fakeIAM{}, ed, preloadedArtifact(), nil, nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			if _, ok := ssmc.params[tc.param]; !ok {
				t.Fatalf("no cache store stored at %s", tc.param)
			}
			got, err := ReadCacheStore(context.Background(), ssmc, tc.class)
			if err != nil {
				t.Fatalf("ReadCacheStore: %v", err)
			}
			if got.Bucket != "ocel-edge-cache" || got.SecretAccessKey != "sha-of-tok-1" {
				t.Errorf("stored store = %+v, want the offered coordinates", got)
			}
		})
	}
}

func TestRun_DanglingCacheStoreTokenFailsBootstrap(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	offer := offeredStore()
	delete(offer, edge.OfferKeySecretAccessKey)
	ed := &fakeEdge{out: edge.BootstrapOutput{
		Trust:  edge.TrustExternal,
		Offers: []edge.Offer{{Kind: edge.OfferCacheStore, Values: offer}},
	}}

	err := Run(context.Background(), cfn, ssmc, iamc, ed, preloadedArtifact(), nil, nil)
	if err == nil {
		t.Fatal("expected Run to fail on an unrecoverable cache-store credential")
	}
	if _, ok := ssmc.params[CacheStoreParamName]; ok {
		t.Error("stored a credential-less cache store despite the hazard")
	}
	if len(cfn.templates) != 0 {
		t.Errorf("provisioned %d stacks despite the hazard", len(cfn.templates))
	}
}

func TestRun_Idempotent(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	ed := &fakeEdge{out: edge.BootstrapOutput{
		Trust:  edge.TrustExternal,
		Values: map[string]string{"namespaceId": "ns-42"},
	}}

	for i := range 2 {
		if err := Run(context.Background(), cfn, ssmc, iamc, ed, preloadedArtifact(), nil, nil); err != nil {
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
	if cfn.creates != 1 {
		t.Errorf("stack was created %d times across two bootstraps, want 1", cfn.creates)
	}
	if cfn.noops != 1 {
		t.Errorf("the second bootstrap submitted %d unchanged templates, want 1: a re-run must converge, not re-provision", cfn.noops)
	}
}

func TestRunPreview_Idempotent(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	values := map[string]string{"namespaceId": "ns-42"}
	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal, Values: values}}

	var passphrase string
	for i := range 2 {
		if err := RunPreview(context.Background(), cfn, ssmc, iamc, ed, preloadedArtifact(), nil, nil); err != nil {
			t.Fatalf("RunPreview %d: %v", i+1, err)
		}
		if i == 0 {
			passphrase = ssmc.params[PassphraseParamName]
		}
	}

	if cfn.creates != 1 {
		t.Errorf("preview stack was created %d times across two bootstraps, want 1", cfn.creates)
	}
	if cfn.noops != 1 {
		t.Errorf("the second bootstrap submitted %d unchanged templates, want 1: a re-run must converge, not re-provision", cfn.noops)
	}
	if len(cfn.templates) != 1 {
		t.Errorf("preview bootstrap touched %d stacks, want only %s", len(cfn.templates), PreviewStackName)
	}

	if len(iamc.created) != 1 {
		t.Errorf("minted %d keys across two preview bootstraps, want 1: %v", len(iamc.created), iamc.created)
	}
	var creds EdgeCredentials
	if err := json.Unmarshal([]byte(ssmc.params[EdgeCredentialsPreviewParamName]), &creds); err != nil {
		t.Fatalf("stored preview credentials are not readable after a re-run: %v", err)
	}
	if creds.AccessKeyID != "AKIAEDGE" {
		t.Errorf("stored key = %q, want the first minted key", creds.AccessKeyID)
	}
	if got := ssmc.params[PassphraseParamName]; got != passphrase {
		t.Error("the second bootstrap regenerated the Pulumi passphrase, orphaning every preview stack encrypted under the first")
	}

	got, err := ReadEdgeValues(context.Background(), ssmc, ClassPreview)
	if err != nil {
		t.Fatalf("ReadEdgeValues: %v", err)
	}
	if got["namespaceId"] != values["namespaceId"] {
		t.Errorf("edge values after a re-run = %v, want %v", got, values)
	}

	tmpl := parseVarsTemplate(t, cfn.templates[PreviewStackName])
	for _, name := range []string{"VarsTable", "VarsKey", "VarsKeyAlias"} {
		if _, ok := tmpl.Resources[name]; !ok {
			t.Errorf("the preview stack no longer declares %s after a re-run", name)
		}
	}
	if got, want := tmpl.Resources["VarsKeyAlias"].Properties.AliasName, varsKeyAliasFor(ClassPreview); got != want {
		t.Errorf("preview key alias = %q, want %q", got, want)
	}
}

func TestRun_EdgeBootstrapFailureStopsProvisioning(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	ed := &fakeEdge{err: errors.New("edge API unreachable")}

	err := Run(context.Background(), cfn, ssmc, iamc, ed, preloadedArtifact(), nil, nil)
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

func TestRun_PublisherFollowsTheISRWriterAdoption(t *testing.T) {
	for _, tc := range []struct {
		name   string
		offers []edge.Offer
		want   bool
	}{
		{"writer adopted", []edge.Offer{{Kind: edge.OfferISRWriter, Values: offeredISRWriter("", "cred")}}, true},
		{"no writer offered", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
			art, _, _ := fixtureArtifactDeps(fixtureArtifact)
			ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal, Offers: tc.offers}}

			pins := stackPins{publisher: fixturePublisherPin()}
			if err := run(context.Background(), cfn, ssmc, iamc, ed, art, pins, productionSubstrate(), nil, nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			for _, name := range []string{
				"TagPublisher", "TagPublisherStream", "TagPublisherRole",
				"TagPublisherDeadLetterQueue",
			} {
				if got := strings.Contains(cfn.templates[StackName], name+":"); got != tc.want {
					t.Errorf("template declares %s = %v, want %v", name, got, tc.want)
				}
			}
		})
	}
}

func TestRun_UnpinnedPublisherSaysWhatStopsReachingTheEdge(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	ed := &fakeEdge{out: edge.BootstrapOutput{
		Trust:  edge.TrustExternal,
		Offers: []edge.Offer{{Kind: edge.OfferISRWriter, Values: offeredISRWriter("", "cred")}},
	}}

	var logged []string
	logf := func(msg string) { logged = append(logged, msg) }
	if err := run(context.Background(), cfn, ssmc, iamc, ed, preloadedArtifact(), stackPins{}, productionSubstrate(), nil, logf); err != nil {
		t.Fatalf("run: %v", err)
	}

	all := strings.Join(logged, "\n")
	if !strings.Contains(all, "no origin-raised invalidation reaches a build's edge replica") {
		t.Errorf("an unpinned publisher does not say what stops reaching the edge:\n%s", all)
	}
	if strings.Contains(all, "the way they did before") {
		t.Errorf("an unpinned publisher still claims invalidations reach the edge as before:\n%s", all)
	}
}
