package bootstrap

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	smithy "github.com/aws/smithy-go"
	"gopkg.in/yaml.v3"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type fakeCFN struct {
	mu         sync.Mutex
	templates  map[string]string
	params     map[string][]cfntypes.Parameter
	statuses   map[string]cfntypes.StackStatus
	outputs    map[string]map[string]string
	tags       map[string][]cfntypes.Tag
	reasons    map[string]string
	holding    map[string]int
	settling   map[string]int
	planned    map[string][]cfntypes.ResourceChange
	changeSets map[string]*fakeChangeSet
	users      *fakeIAM
	applied    []string
	deleted    []string
	events     []string
	planning   []string
	executed   []string
	discarded  []string
	creates    int
	updates    int
	restamps   int
	noops      int
}

func newFakeCFN() *fakeCFN {
	return &fakeCFN{
		templates:  map[string]string{},
		params:     map[string][]cfntypes.Parameter{},
		statuses:   map[string]cfntypes.StackStatus{},
		outputs:    map[string]map[string]string{},
		tags:       map[string][]cfntypes.Tag{},
		reasons:    map[string]string{},
		holding:    map[string]int{},
		settling:   map[string]int{},
		planned:    map[string][]cfntypes.ResourceChange{},
		changeSets: map[string]*fakeChangeSet{},
	}
}

func (f *fakeCFN) holdName(stackName string, creates int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.holding[stackName] = creates
}

func (f *fakeCFN) reason(stackName, why string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reasons[stackName] = why
}

func (f *fakeCFN) wedge(stackName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[stackName] = cfntypes.StackStatusRollbackComplete
	f.outputs[stackName] = map[string]string{}
}

type validationError struct{ msg string }

func (e validationError) Error() string                 { return e.msg }
func (e validationError) ErrorCode() string             { return "ValidationError" }
func (e validationError) ErrorMessage() string          { return e.msg }
func (e validationError) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

func (f *fakeCFN) DescribeStacks(_ context.Context, in *cloudformation.DescribeStacksInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.StackName)
	if _, ok := f.templates[name]; !ok {
		return nil, validationError{msg: "Stack with id " + name + " does not exist"}
	}
	if looks := f.settling[name]; looks > 0 {
		if f.settling[name] = looks - 1; f.settling[name] == 0 {
			f.statuses[name] = cfntypes.StackStatusUpdateComplete
		}
	}
	var out []cfntypes.Output
	for k, v := range f.outputs[name] {
		out = append(out, cfntypes.Output{OutputKey: aws.String(k), OutputValue: aws.String(v)})
	}
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{
		StackName:   aws.String(name),
		StackStatus: f.statuses[name],
		Outputs:     out,
		Tags:        f.tags[name],
	}}}, nil
}

func (f *fakeCFN) CreateStack(_ context.Context, in *cloudformation.CreateStackInput, _ ...func(*cloudformation.Options)) (*cloudformation.CreateStackOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	name := aws.ToString(in.StackName)
	f.record(name, aws.ToString(in.TemplateBody), in.Parameters, in.Tags)
	if f.holding[name] > 0 {
		f.holding[name]--
		f.statuses[name] = cfntypes.StackStatusRollbackComplete
		if _, ok := f.reasons[name]; !ok {
			f.reasons[name] = "The following resource(s) failed to create: [RevalidateQueue]. " + heldQueueName
		}
		f.outputs[name] = map[string]string{}
		return &cloudformation.CreateStackOutput{}, nil
	}
	f.statuses[name] = cfntypes.StackStatusCreateComplete
	return &cloudformation.CreateStackOutput{}, nil
}

func (f *fakeCFN) UpdateStack(ctx context.Context, in *cloudformation.UpdateStackInput, _ ...func(*cloudformation.Options)) (*cloudformation.UpdateStackOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.StackName)
	if _, ok := f.templates[name]; !ok {
		return nil, validationError{msg: "Stack with id " + name + " does not exist"}
	}
	if sameStackTags(f.tags[name], in.Tags) {
		return nil, validationError{msg: "No updates are to be performed."}
	}
	f.restamps++
	f.tags[name] = in.Tags
	f.params[name] = in.Parameters
	f.statuses[name] = cfntypes.StackStatusUpdateComplete
	return &cloudformation.UpdateStackOutput{}, nil
}

func (f *fakeCFN) DescribeStackEvents(_ context.Context, in *cloudformation.DescribeStackEventsInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStackEventsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reason, ok := f.reasons[aws.ToString(in.StackName)]
	if !ok {
		return &cloudformation.DescribeStackEventsOutput{}, nil
	}
	return &cloudformation.DescribeStackEventsOutput{StackEvents: []cfntypes.StackEvent{{
		ResourceStatusReason: aws.String(reason),
	}}}, nil
}

func (f *fakeCFN) ListStackResources(_ context.Context, in *cloudformation.ListStackResourcesInput, _ ...func(*cloudformation.Options)) (*cloudformation.ListStackResourcesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.StackName)
	body, ok := f.templates[name]
	if !ok {
		return nil, validationError{msg: "Stack with id " + name + " does not exist"}
	}
	out := &cloudformation.ListStackResourcesOutput{}
	for _, resource := range templateResources(body) {
		out.StackResourceSummaries = append(out.StackResourceSummaries, cfntypes.StackResourceSummary{
			LogicalResourceId: aws.String(resource.id),
			ResourceType:      aws.String(resource.kind),
		})
	}
	return out, nil
}

type fakeChangeSet struct {
	stack   string
	body    string
	params  []cfntypes.Parameter
	tags    []cfntypes.Tag
	changes []cfntypes.ResourceChange
	empty   bool
}

func (f *fakeCFN) plan(stackName string, changes ...cfntypes.ResourceChange) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.planned[stackName] = changes
}

func (f *fakeCFN) busy(stackName string, looks int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[stackName] = cfntypes.StackStatusUpdateInProgress
	f.settling[stackName] = looks
}

const behindTemplate = "AWSTemplateFormatVersion: '2010-09-09'\nResources: {}\n"

func (f *fakeCFN) fallBehind(stackName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.templates[stackName] = behindTemplate
	f.tags[stackName] = stampTags(Stamp{Schema: RequiredSchema, Digest: TemplateDigest(behindTemplate), WrittenBy: "1.0.0"})
}

func (f *fakeCFN) CreateChangeSet(_ context.Context, in *cloudformation.CreateChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.CreateChangeSetOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name, body := aws.ToString(in.StackName), aws.ToString(in.TemplateBody)
	id := fmt.Sprintf("%s/%s", name, aws.ToString(in.ChangeSetName))
	set := &fakeChangeSet{stack: name, body: body, params: in.Parameters, tags: in.Tags, changes: f.planned[name]}
	if f.templates[name] == body && sameParams(f.params[name], in.Parameters) {
		set.empty = true
		f.noops++
	}
	f.changeSets[id] = set
	f.planning = append(f.planning, id)
	return &cloudformation.CreateChangeSetOutput{Id: aws.String(id)}, nil
}

func (f *fakeCFN) DescribeChangeSet(_ context.Context, in *cloudformation.DescribeChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeChangeSetOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	set, ok := f.changeSets[aws.ToString(in.ChangeSetName)]
	if !ok {
		return nil, validationError{msg: "ChangeSet " + aws.ToString(in.ChangeSetName) + " does not exist"}
	}
	if set.empty {
		return &cloudformation.DescribeChangeSetOutput{
			Status:       cfntypes.ChangeSetStatusFailed,
			StatusReason: aws.String("The submitted information didn't contain changes."),
		}, nil
	}
	out := &cloudformation.DescribeChangeSetOutput{Status: cfntypes.ChangeSetStatusCreateComplete}
	for i := range set.changes {
		out.Changes = append(out.Changes, cfntypes.Change{ResourceChange: &set.changes[i]})
	}
	return out, nil
}

func (f *fakeCFN) ExecuteChangeSet(ctx context.Context, in *cloudformation.ExecuteChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.ExecuteChangeSetOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.ChangeSetName)
	set, ok := f.changeSets[id]
	if !ok {
		return nil, validationError{msg: "ChangeSet " + id + " does not exist"}
	}
	f.updates++
	f.executed = append(f.executed, id)
	delete(f.changeSets, id)
	f.record(set.stack, set.body, set.params, set.tags)
	f.statuses[set.stack] = cfntypes.StackStatusUpdateComplete
	return &cloudformation.ExecuteChangeSetOutput{}, nil
}

func (f *fakeCFN) DeleteChangeSet(ctx context.Context, in *cloudformation.DeleteChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.DeleteChangeSetOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.ChangeSetName)
	delete(f.changeSets, id)
	f.discarded = append(f.discarded, id)
	return &cloudformation.DeleteChangeSetOutput{}, nil
}

func (f *fakeCFN) leftBehind() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, id := range f.planning {
		if !slices.Contains(f.executed, id) && !slices.Contains(f.discarded, id) {
			out = append(out, id)
		}
	}
	return out
}

func (f *fakeCFN) DeleteStack(_ context.Context, in *cloudformation.DeleteStackInput, _ ...func(*cloudformation.Options)) (*cloudformation.DeleteStackOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.StackName)
	if user, ok := declaredUser(f.templates[name]); ok && f.users != nil && len(f.users.keys) > 0 {
		return nil, validationError{msg: "Stack " + name + " is DELETE_FAILED: cannot delete IAM user " + user + " while it holds an access key created outside CloudFormation"}
	}
	f.deleted = append(f.deleted, name)
	f.events = append(f.events, "removed "+name)
	delete(f.templates, name)
	delete(f.outputs, name)
	delete(f.tags, name)
	delete(f.params, name)
	delete(f.statuses, name)
	delete(f.reasons, name)
	return &cloudformation.DeleteStackOutput{}, nil
}

func (f *fakeCFN) stampOf(stackName string) Stamp {
	f.mu.Lock()
	defer f.mu.Unlock()
	return readStamp(f.tags[stackName])
}

func declaredUser(template string) (string, bool) {
	var tmpl struct {
		Resources map[string]struct {
			Type       string `yaml:"Type"`
			Properties struct {
				UserName string `yaml:"UserName"`
			} `yaml:"Properties"`
		} `yaml:"Resources"`
	}
	if yaml.Unmarshal([]byte(template), &tmpl) != nil {
		return "", false
	}
	for _, r := range tmpl.Resources {
		if r.Type == "AWS::IAM::User" {
			return r.Properties.UserName, true
		}
	}
	return "", false
}

func (f *fakeCFN) record(stackName, body string, params []cfntypes.Parameter, tags []cfntypes.Tag) {
	f.templates[stackName] = body
	f.params[stackName] = params
	f.tags[stackName] = tags
	f.applied = append(f.applied, stackName)
	f.events = append(f.events, "wrote "+stackName)

	var tmpl struct {
		Outputs map[string]struct {
			Value string `yaml:"Value"`
		} `yaml:"Outputs"`
	}
	if err := yaml.Unmarshal([]byte(body), &tmpl); err != nil {
		panic("fake CloudFormation was handed a template it cannot parse: " + err.Error())
	}
	out := f.outputs[stackName]
	if out == nil {
		out = map[string]string{}
		f.outputs[stackName] = out
	}
	for key, o := range tmpl.Outputs {
		out[key] = syntheticOutput(stackName, key, o.Value)
	}
}

func syntheticOutput(stackName, key, declared string) string {
	switch key {
	case outputInfraClass:
		return declared
	case outputStateBucket:
		return "ocel-state-test"
	case outputArtifactBucket:
		return "ocel-artifacts-test"
	case outputAssetBucket:
		return "ocel-assets-test"
	case outputStateTable:
		return "ocel-statetable-test"
	}
	if strings.HasSuffix(key, "Arn") {
		return "arn:aws:test:us-east-1:111122223333:" + stackName + "/" + key
	}
	return "https://" + strings.ToLower(key) + ".test/" + stackName
}

func sameParams(a, b []cfntypes.Parameter) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if aws.ToString(a[i].ParameterKey) != aws.ToString(b[i].ParameterKey) ||
			aws.ToString(a[i].ParameterValue) != aws.ToString(b[i].ParameterValue) {
			return false
		}
	}
	return true
}

func (f *fakeCFN) seed(stackName, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(stackName, body, nil, nil)
	f.applied = f.applied[:len(f.applied)-1]
	f.statuses[stackName] = cfntypes.StackStatusCreateComplete
}

func (f *fakeCFN) output(stackName, key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.outputs[stackName][key]
}

func (f *fakeCFN) template(stackName string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.templates[stackName]
}

func (f *fakeCFN) parameter(stackName, key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.params[stackName] {
		if aws.ToString(p.ParameterKey) == key {
			return aws.ToString(p.ParameterValue)
		}
	}
	return ""
}

func (f *fakeCFN) stacks() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for name := range f.templates {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (f *fakeCFN) lastEvent(event string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.events) - 1; i >= 0; i-- {
		if f.events[i] == event {
			return i
		}
	}
	return -1
}

func (f *fakeCFN) firstApplied(stackName string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Index(f.applied, stackName)
}

type fakeEdge struct {
	kind        edge.Kind
	needs       []edge.Need
	out         edge.BootstrapOutput
	err         error
	onBootstrap func()
	bootstraps  int
	class       edge.Class
}

func (f *fakeEdge) Kind() edge.Kind {
	if f.kind == "" {
		return "fake"
	}
	return f.kind
}

func (f *fakeEdge) Bootstrap(_ context.Context, class edge.Class) (edge.BootstrapOutput, error) {
	f.bootstraps++
	f.class = class
	if f.onBootstrap != nil {
		f.onBootstrap()
	}
	return f.out, f.err
}

func (f *fakeEdge) Teardown(context.Context, edge.Class) error { return nil }

func (f *fakeEdge) Facts() edge.Facts { return edge.Facts{} }

func (f *fakeEdge) Supported() []edge.Need { return slices.Clone(f.needs) }

func (f *fakeEdge) FlipBound() edge.FlipBound { return edge.FlipBound{} }

func (f *fakeEdge) Reconcile(context.Context, edge.StackSpec, edge.StackState) (edge.EdgeStack, error) {
	return nil, errors.New("bootstrap never reconciles a project stack")
}

func (f *fakeEdge) Open(edge.StackState) (edge.EdgeStack, error) {
	return nil, errors.New("bootstrap never opens a project stack")
}

func (f *fakeEdge) ReconcilePreviewWildcard(context.Context, edge.PreviewWildcardSpec) (string, error) {
	return "", errors.New("bootstrap never reconciles the preview wildcard")
}

func (f *fakeEdge) DestroyPreviewWildcard(context.Context, string) error {
	return errors.New("bootstrap never destroys the preview wildcard")
}

func (f *fakeEdge) ProjectSurfaces(edge.ProjectScope) []edge.Surface { return nil }

func (f *fakeEdge) PreviewWildcardSurfaces(string) (edge.Surface, edge.Surface) {
	return edge.Surface{}, edge.Surface{}
}

func (f *fakeEdge) SharedPreviewSurface() edge.Surface { return edge.Surface{} }

func (f *fakeEdge) DomainOwner(context.Context, string) (string, error) {
	return "", errors.New("bootstrap never reads a domain owner")
}

var standingEdge = func() edge.Edge { return &fakeEdge{kind: "default"} }

func frontedBy(t *testing.T, ed edge.Edge) {
	t.Helper()
	previous := standingEdge
	standingEdge = func() edge.Edge { return ed }
	t.Cleanup(func() { standingEdge = previous })
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

func assertMintedSecrets(t *testing.T, ssmc *fakeSSM, names ...string) {
	t.Helper()
	for _, name := range names {
		value, ok := ssmc.params[name]
		if !ok {
			t.Errorf("bootstrap left %s empty; every deploy that reads it refuses", name)
			continue
		}
		if _, err := hex.DecodeString(value); err != nil || len(value) != 64 {
			t.Errorf("%s = %q, want 32 random bytes", name, value)
		}
	}
}

func apisOf(cfn *fakeCFN, ssmc *fakeSSM, iamc *fakeIAM, store ObjectStore) APIs {
	return apisFronting(cfn, ssmc, iamc, store, standingEdge())
}

func apisFronting(cfn *fakeCFN, ssmc *fakeSSM, iamc *fakeIAM, store ObjectStore, front edge.Edge) APIs {
	cfn.users = iamc
	return APIs{CFN: cfn, SSM: ssmc, IAM: iamc, Store: store, Edge: front}
}

func everything() Request { return Request{Features: featureNames()} }

func runAll(ctx context.Context, apis APIs, target spec) error {
	return run(ctx, apis, target, everything(), nil, nil)
}

func isrStack(class string) string  { return FeatureStackName(FeatureISR, class) }
func edgeStack(class string) string { return FeatureStackName(FeatureCloudflareEdge, class) }
func optStack(class string) string {
	return FeatureStackName(FeatureImageOptimization, class)
}

func TestRun(t *testing.T) {
	t.Run("core alone stands up no feature stack", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		ed := &fakeEdge{}
		frontedBy(t, ed)

		if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), ClassProduction, Request{}, nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := cfn.stacks(); !slices.Equal(got, []string{StackName}) {
			t.Errorf("stacks = %v, want only %s", got, StackName)
		}
		if ed.bootstraps != 1 {
			t.Errorf("the front was bootstrapped %d times for a core-only bootstrap, want 1: the edge is the front, not a feature", ed.bootstraps)
		}
		if len(iamc.created) != 0 {
			t.Errorf("minted %v for a core-only bootstrap", iamc.created)
		}
	})

	t.Run("asking for the cloudflare edge pulls in what it stands on", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), ClassProduction,
			Request{Features: []string{FeatureISR, FeatureCloudflareEdge}}, nil, nil)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		want := []string{StackName, isrStack(ClassProduction), edgeStack(ClassProduction)}
		slices.Sort(want)
		if got := cfn.stacks(); !slices.Equal(got, want) {
			t.Errorf("stacks = %v, want %v", got, want)
		}
	})

	t.Run("a stack is never applied before what feeds it", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), ClassProduction, everything(), nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		core := cfn.firstApplied(StackName)
		isr := cfn.firstApplied(isrStack(ClassProduction))
		front := cfn.firstApplied(edgeStack(ClassProduction))
		if core < 0 || isr < 0 || front < 0 {
			t.Fatalf("applied %v, want core, isr and the cloudflare edge among them", cfn.applied)
		}
		if !(core < isr && isr < front) {
			t.Errorf("applied in order %v, want core before isr before the cloudflare edge", cfn.applied)
		}
	})

	t.Run("a feature reads its upstream by parameter, never by import", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), ClassProduction, everything(), nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		front := edgeStack(ClassProduction)
		if got := cfn.parameter(front, paramRevalidateQueueARN); got != cfn.output(isrStack(ClassProduction), outputRevalidateQueueARN) {
			t.Errorf("%s was handed queue ARN %q, want the one the isr stack output", front, got)
		}
		if got := cfn.parameter(front, paramImageOptimizerARN); got != cfn.output(optStack(ClassProduction), outputImageOptimizerARN) {
			t.Errorf("%s was handed optimizer ARN %q, want the one the image-optimization stack output", front, got)
		}
		for _, name := range cfn.stacks() {
			if body := cfn.template(name); strings.Contains(body, "ImportValue") || strings.Contains(body, "Export:") {
				t.Errorf("%s reaches across stacks with a CloudFormation export", name)
			}
		}
	})

	t.Run("the cloudflare edge feature provisions the edge reader", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		ed := &fakeEdge{kind: "cloudflare"}
		frontedBy(t, ed)

		if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), ClassProduction, everything(), nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if ed.bootstraps != 1 {
			t.Errorf("edge bootstrapped %d times, want exactly 1", ed.bootstraps)
		}
		if !hasEdgeUser(t, cfn.template(edgeStack(ClassProduction))) {
			t.Error("the cloudflare edge feature did not provision the edge reader IAM user")
		}
		if hasEdgeUser(t, cfn.template(StackName)) {
			t.Error("core provisioned an edge reader; only the edge that needs one may")
		}
		if len(iamc.created) != 1 || iamc.created[0] != EdgeUserName {
			t.Errorf("minted keys for %v, want [%s]", iamc.created, EdgeUserName)
		}
		if _, ok := ssmc.params[cloudflareNames(ClassProduction).credentialsParam]; !ok {
			t.Errorf("no static key stored at %s", cloudflareNames(ClassProduction).credentialsParam)
		}
	})

	t.Run("mints the secrets a release and its publisher authenticate with", func(t *testing.T) {
		ssmc := newFakeSSM()
		frontedBy(t, &fakeEdge{
			kind: "cloudflare",
			out:  edge.BootstrapOutput{Offers: []edge.Offer{{Kind: edge.OfferISRWriter, Values: offeredISRWriter("", "cred-prod")}}},
		})

		if err := Run(context.Background(), apisOf(newFakeCFN(), ssmc, &fakeIAM{}, preloadedStore()), ClassProduction, everything(), nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		assertMintedSecrets(t, ssmc, OriginSecretParamName, cloudflareNames(ClassProduction).isrWriterSeedParam)
	})

	t.Run("bootstraps the edge for its own class", func(t *testing.T) {
		for _, want := range []edge.Class{edge.ClassProduction, edge.ClassPreview} {
			t.Run(string(want), func(t *testing.T) {
				ed := &fakeEdge{kind: "cloudflare"}
				frontedBy(t, ed)
				if err := Run(context.Background(), apisOf(newFakeCFN(), newFakeSSM(), &fakeIAM{}, preloadedStore()), string(want), everything(), nil, nil); err != nil {
					t.Fatalf("run: %v", err)
				}
				if ed.class != want {
					t.Errorf("edge bootstrapped for class %q, want %q", ed.class, want)
				}
			})
		}
	})

	t.Run("no cloudflare edge leaves no credential", func(t *testing.T) {
		for _, tc := range []struct {
			class     string
			credParam string
		}{
			{ClassProduction, cloudflareNames(ClassProduction).credentialsParam},
			{ClassPreview, cloudflareNames(ClassPreview).credentialsParam},
		} {
			t.Run(tc.class, func(t *testing.T) {
				cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
				frontedBy(t, &fakeEdge{})

				req := Request{Features: []string{FeatureISR, FeatureImageOptimization}}
				if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), tc.class, req, nil, nil); err != nil {
					t.Fatalf("run: %v", err)
				}
				for _, name := range cfn.stacks() {
					if hasEdgeUser(t, cfn.template(name)) {
						t.Errorf("%s provisioned an IAM user with no external edge asked for", name)
					}
				}
				if len(iamc.created) != 0 {
					t.Errorf("minted static keys for %v with no external edge asked for", iamc.created)
				}
				if _, ok := ssmc.params[tc.credParam]; ok {
					t.Errorf("stored a static key at %s with no external edge asked for", tc.credParam)
				}
			})
		}
	})

	t.Run("persists edge values", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		values := map[string]string{"bucketName": "edge-cache-7f3", "namespaceId": "ns-42"}
		frontedBy(t, &fakeEdge{kind: "cloudflare", out: edge.BootstrapOutput{Values: values}})

		if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), ClassProduction, everything(), nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		got, err := ReadEdgeValues(context.Background(), ssmc, ClassProduction, KindCloudflare)
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
	})

	t.Run("no edge values stores nothing", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), ClassProduction, everything(), nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if _, ok := ssmc.params[cloudflareNames(ClassProduction).valuesParam]; ok {
			t.Errorf("stored an edge values parameter for an edge that reported none")
		}
		got, err := ReadEdgeValues(context.Background(), ssmc, ClassProduction, KindCloudflare)
		if err != nil {
			t.Fatalf("ReadEdgeValues on an absent parameter: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("ReadEdgeValues = %v, want empty", got)
		}
	})

	t.Run("ignores unrecognised offer", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare", out: edge.BootstrapOutput{
			Offers: []edge.Offer{
				{Kind: "something-invented-later", Values: map[string]string{"id": "x"}},
				{Kind: edge.OfferCacheStore, Values: offeredStore()},
			},
		}})

		if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), ClassProduction, everything(), nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !hasEdgeUser(t, cfn.template(edgeStack(ClassProduction))) {
			t.Error("an unrecognised offer changed what was provisioned")
		}
		if len(iamc.created) != 1 {
			t.Errorf("an unrecognised offer changed what was minted: %v", iamc.created)
		}
		if _, ok := ssmc.params[cloudflareNames(ClassProduction).cacheStoreParam]; !ok {
			t.Errorf("the recognised offer alongside it was not adopted")
		}
	})

	t.Run("no offers stores no cache store", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), ClassProduction, everything(), nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if _, ok := ssmc.params[cloudflareNames(ClassProduction).cacheStoreParam]; ok {
			t.Errorf("stored a cache store for an edge that offered none")
		}
	})

	t.Run("adopts cache store per class", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			class string
			param string
		}{
			{"production", ClassProduction, cloudflareNames(ClassProduction).cacheStoreParam},
			{"preview", ClassPreview, cloudflareNames(ClassPreview).cacheStoreParam},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ssmc := newFakeSSM()
				frontedBy(t, &fakeEdge{kind: "cloudflare", out: edge.BootstrapOutput{
					Offers: []edge.Offer{{Kind: edge.OfferCacheStore, Values: offeredStore()}},
				}})

				if err := Run(context.Background(), apisOf(newFakeCFN(), ssmc, &fakeIAM{}, preloadedStore()), tc.class, everything(), nil, nil); err != nil {
					t.Fatalf("run: %v", err)
				}
				if _, ok := ssmc.params[tc.param]; !ok {
					t.Fatalf("no cache store stored at %s", tc.param)
				}
				got, err := ReadCacheStore(context.Background(), ssmc, tc.class, KindCloudflare)
				if err != nil {
					t.Fatalf("ReadCacheStore: %v", err)
				}
				if got.Bucket != "ocel-edge-cache" || got.SecretAccessKey != "sha-of-tok-1" {
					t.Errorf("stored store = %+v, want the offered coordinates", got)
				}
			})
		}
	})

	t.Run("dangling cache store token fails bootstrap", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		offer := offeredStore()
		delete(offer, edge.OfferKeySecretAccessKey)
		frontedBy(t, &fakeEdge{kind: "cloudflare", out: edge.BootstrapOutput{
			Offers: []edge.Offer{{Kind: edge.OfferCacheStore, Values: offer}},
		}})

		err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), ClassProduction, everything(), nil, nil)
		if err == nil {
			t.Fatal("expected Run to fail on an unrecoverable cache-store credential")
		}
		if _, ok := ssmc.params[cloudflareNames(ClassProduction).cacheStoreParam]; ok {
			t.Error("stored a credential-less cache store despite the hazard")
		}
		if slices.Contains(cfn.stacks(), edgeStack(ClassProduction)) {
			t.Error("provisioned the edge stack despite the hazard")
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare", out: edge.BootstrapOutput{
			Values: map[string]string{"namespaceId": "ns-42"},
		}})

		for i := range 2 {
			if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), ClassProduction, everything(), nil, nil); err != nil {
				t.Fatalf("Run %d: %v", i+1, err)
			}
		}
		if len(iamc.created) != 1 {
			t.Errorf("minted %d keys across two bootstraps, want 1: %v", len(iamc.created), iamc.created)
		}
		var creds EdgeCredentials
		if err := json.Unmarshal([]byte(ssmc.params[cloudflareNames(ClassProduction).credentialsParam]), &creds); err != nil {
			t.Fatalf("stored credentials are not readable after a re-run: %v", err)
		}
		if creds.AccessKeyID != "AKIAEDGE" {
			t.Errorf("stored key = %q, want the first minted key", creds.AccessKeyID)
		}
		if want := 1 + len(featureNames()); cfn.creates != want {
			t.Errorf("stacks were created %d times across two bootstraps, want one create each for core and its %d features", cfn.creates, len(featureNames()))
		}
		if want := 1 + len(featureNames()); cfn.noops != want {
			t.Errorf("the second bootstrap submitted %d unchanged templates, want %d: a re-run must converge, not re-provision", cfn.noops, want)
		}
	})

	t.Run("edge bootstrap failure stops provisioning the feature it belongs to", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare", err: errors.New("edge API unreachable")})

		err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), ClassProduction, everything(), nil, nil)
		if err == nil {
			t.Fatal("expected Run to fail when the edge bootstrap fails")
		}
		if slices.Contains(cfn.stacks(), edgeStack(ClassProduction)) {
			t.Error("provisioned the edge stack despite a failed edge bootstrap")
		}
		if len(iamc.created) != 0 {
			t.Errorf("minted %v despite a failed edge bootstrap", iamc.created)
		}
	})

	t.Run("a dropped name takes its stack with it, in the order it was handed", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})
		apis := apisOf(cfn, ssmc, iamc, preloadedStore())

		if err := Run(context.Background(), apis, ClassProduction, everything(), nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		dropped := Request{
			Features: []string{FeatureImageOptimization},
			Remove:   []string{FeatureCloudflareEdge, FeatureISR},
		}
		if err := Run(context.Background(), apis, ClassProduction, dropped, nil, nil); err != nil {
			t.Fatalf("Run without the edge: %v", err)
		}
		want := []string{edgeStack(ClassProduction), isrStack(ClassProduction)}
		if !slices.Equal(cfn.deleted, want) {
			t.Errorf("deleted %v, want %v: a dependent goes before what it stands on", cfn.deleted, want)
		}
		if !slices.Contains(cfn.stacks(), optStack(ClassProduction)) {
			t.Errorf("stacks left = %v, want the feature that stayed in the set", cfn.stacks())
		}
	})

}

func TestRunPreview(t *testing.T) {
	t.Run("mints the preview secrets a release and its publisher authenticate with", func(t *testing.T) {
		ssmc := newFakeSSM()
		frontedBy(t, &fakeEdge{kind: "cloudflare", out: edge.BootstrapOutput{
			Offers: []edge.Offer{{Kind: edge.OfferISRWriter, Values: offeredISRWriter("-preview", "cred-preview")}},
		}})

		if err := Run(context.Background(), apisOf(newFakeCFN(), ssmc, &fakeIAM{}, preloadedStore()), ClassPreview, everything(), nil, nil); err != nil {
			t.Fatalf("RunPreview: %v", err)
		}
		assertMintedSecrets(t, ssmc, OriginSecretPreviewParamName, cloudflareNames(ClassPreview).isrWriterSeedParam)
		for _, name := range []string{OriginSecretParamName, cloudflareNames(ClassProduction).isrWriterSeedParam} {
			if _, ok := ssmc.params[name]; ok {
				t.Errorf("a preview bootstrap wrote %s, want the production secrets left alone", name)
			}
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		values := map[string]string{"namespaceId": "ns-42"}
		frontedBy(t, &fakeEdge{kind: "cloudflare", out: edge.BootstrapOutput{Values: values}})

		var passphrase string
		for i := range 2 {
			if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), ClassPreview, everything(), nil, nil); err != nil {
				t.Fatalf("RunPreview %d: %v", i+1, err)
			}
			if i == 0 {
				passphrase = ssmc.params[PassphraseParamName]
			}
		}

		if want := 1 + len(featureNames()); cfn.creates != want {
			t.Errorf("stacks were created %d times across two preview bootstraps, want one create each for core and its %d features", cfn.creates, len(featureNames()))
		}
		if want := 1 + len(featureNames()); cfn.noops != want {
			t.Errorf("the second bootstrap submitted %d unchanged templates, want %d: a re-run must converge, not re-provision", cfn.noops, want)
		}
		for _, name := range cfn.stacks() {
			if !strings.HasSuffix(name, "-preview") {
				t.Errorf("a preview bootstrap touched %s, want only preview-suffixed stacks", name)
			}
		}

		if len(iamc.created) != 1 {
			t.Errorf("minted %d keys across two preview bootstraps, want 1: %v", len(iamc.created), iamc.created)
		}
		var creds EdgeCredentials
		if err := json.Unmarshal([]byte(ssmc.params[cloudflareNames(ClassPreview).credentialsParam]), &creds); err != nil {
			t.Fatalf("stored preview credentials are not readable after a re-run: %v", err)
		}
		if creds.AccessKeyID != "AKIAEDGE" {
			t.Errorf("stored key = %q, want the first minted key", creds.AccessKeyID)
		}
		if got := ssmc.params[PassphraseParamName]; got != passphrase {
			t.Error("the second bootstrap regenerated the Pulumi passphrase, orphaning every preview stack encrypted under the first")
		}

		got, err := ReadEdgeValues(context.Background(), ssmc, ClassPreview, KindCloudflare)
		if err != nil {
			t.Fatalf("ReadEdgeValues: %v", err)
		}
		if got["namespaceId"] != values["namespaceId"] {
			t.Errorf("edge values after a re-run = %v, want %v", got, values)
		}

		tmpl := parseVarsTemplate(t, cfn.template(PreviewStackName))
		for _, name := range []string{"VarsTable", "VarsKey", "VarsKeyAlias"} {
			if _, ok := tmpl.Resources[name]; !ok {
				t.Errorf("the preview stack no longer declares %s after a re-run", name)
			}
		}
		if got, want := tmpl.Resources["VarsKeyAlias"].Properties.AliasName, varsKeyAliasFor(ClassPreview); got != want {
			t.Errorf("preview key alias = %q, want %q", got, want)
		}
	})
}

func TestRunDefaultEdge(t *testing.T) {
	t.Run("core bootstraps the edge the provider fronts with by default", func(t *testing.T) {
		front := &fakeEdge{kind: "cloudfront"}
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		apis := apisFronting(cfn, ssmc, iamc, preloadedStore(), front)
		if err := Run(context.Background(), apis, ClassProduction, Request{}, nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if front.bootstraps != 1 {
			t.Errorf("the default edge was bootstrapped %d times, want 1: nothing else stands its shared resources up", front.bootstraps)
		}
		if front.class != edge.ClassProduction {
			t.Errorf("the default edge was bootstrapped for class %q, want %q", front.class, edge.ClassProduction)
		}
	})

	t.Run("the default edge sees the core it reads before it runs", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		var sawCore bool
		front := &fakeEdge{kind: "cloudfront", onBootstrap: func() {
			deployed, err := CheckDeployed(context.Background(), cfn)
			if err != nil {
				t.Errorf("CheckDeployed inside the edge bootstrap: %v", err)
			}
			sawCore = deployed.AssetBucket != ""
		}}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		if err := Run(context.Background(), apisFronting(cfn, ssmc, iamc, preloadedStore(), front), ClassProduction, Request{}, nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !sawCore {
			t.Error("the default edge was bootstrapped before the core it reads the asset bucket from")
		}
	})

	t.Run("preview bootstraps the default edge for the preview class", func(t *testing.T) {
		front := &fakeEdge{kind: "cloudfront"}
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		if err := Run(context.Background(), apisFronting(cfn, ssmc, iamc, preloadedStore(), front), ClassPreview, Request{}, nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if front.class != edge.ClassPreview {
			t.Errorf("the default edge was bootstrapped for class %q, want %q", front.class, edge.ClassPreview)
		}
	})

	t.Run("a default edge that cannot be bootstrapped stops the run", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		front := &fakeEdge{kind: "cloudfront", err: errors.New("edge API unreachable")}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		err := Run(context.Background(), apisFronting(cfn, ssmc, iamc, preloadedStore(), front), ClassProduction, everything(), nil, nil)
		if err == nil {
			t.Fatal("expected Run to fail when the default edge cannot be bootstrapped")
		}
		if slices.Contains(cfn.stacks(), isrStack(ClassProduction)) {
			t.Error("provisioned feature stacks on top of an edge that never came up")
		}
	})
}

func TestRunDropsCloudflareEdge(t *testing.T) {
	t.Run("the edge's key and stored handles go before its stack", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			class string
		}{
			{"production", ClassProduction},
			{"preview", ClassPreview},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
				frontedBy(t, &fakeEdge{kind: "cloudflare", out: edge.BootstrapOutput{
					Values: map[string]string{"namespaceId": "ns-42"},
					Offers: []edge.Offer{
						{Kind: edge.OfferCacheStore, Values: offeredStore()},
						{Kind: edge.OfferDeploymentsStore, Values: offeredDeploymentsStore()},
						{Kind: edge.OfferISRWriter, Values: offeredISRWriter(previewSuffix(tc.class), "cred")},
					},
				}})
				apis := apisOf(cfn, ssmc, iamc, preloadedStore())

				if err := Run(context.Background(), apis, tc.class, everything(), nil, nil); err != nil {
					t.Fatalf("run: %v", err)
				}
				names, err := edgeNamesFor(tc.class, KindCloudflare)
				if err != nil {
					t.Fatalf("edgeNamesFor: %v", err)
				}
				for _, param := range names.edgeParams() {
					if _, ok := ssmc.params[param]; !ok {
						t.Fatalf("%s was never stored, so dropping it proves nothing", param)
					}
				}

				req := Request{
					Features: []string{FeatureISR, FeatureImageOptimization},
					Remove:   []string{FeatureCloudflareEdge},
				}
				if err := Run(context.Background(), apis, tc.class, req, nil, nil); err != nil {
					t.Fatalf("dropping the cloudflare edge: %v", err)
				}
				if len(iamc.keys) != 0 {
					t.Errorf("edge reader %s still holds %v after its stack was dropped", names.user, iamc.keys)
				}
				for _, param := range names.edgeParams() {
					if _, ok := ssmc.params[param]; ok {
						t.Errorf("%s survived the drop, keeping a live handle to an edge that is gone", param)
					}
				}
				if !slices.Contains(cfn.deleted, edgeStack(tc.class)) {
					t.Errorf("deleted %v, want the cloudflare edge stack among them", cfn.deleted)
				}
				if _, ok := ssmc.params[originSecretFor(t, tc.class)]; !ok {
					t.Error("dropping the edge took the origin secret with it; core owns that")
				}
			})
		}
	})

	t.Run("dropping isr takes the edge that stands on it down first", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})
		apis := apisOf(cfn, ssmc, iamc, preloadedStore())

		if err := Run(context.Background(), apis, ClassProduction, everything(), nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		req := Request{
			Features: []string{FeatureImageOptimization},
			Remove:   []string{FeatureCloudflareEdge, FeatureISR},
		}
		if err := Run(context.Background(), apis, ClassProduction, req, nil, nil); err != nil {
			t.Fatalf("dropping isr: %v", err)
		}
		if len(iamc.keys) != 0 {
			t.Errorf("the edge reader still holds %v after the closure took its stack", iamc.keys)
		}
	})
}

func previewSuffix(class string) string {
	if class == ClassPreview {
		return "-preview"
	}
	return ""
}

func originSecretFor(t *testing.T, class string) string {
	t.Helper()
	names, err := edgeNamesFor(class, KindCloudflare)
	if err != nil {
		t.Fatalf("edgeNamesFor: %v", err)
	}
	return names.originSecretParam
}

func TestUpsertRecoversFailedStacks(t *testing.T) {
	t.Run("a rolled-back feature stack reads as absent, not enabled", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})

		cfn.holdName(isrStack(ClassProduction), 1)
		holdNothing(t)
		if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), ClassProduction,
			Request{Features: []string{FeatureISR}}, nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}

		cfn.wedge(isrStack(ClassProduction))
		deployed, err := CheckDeployed(context.Background(), cfn)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		if deployed.Features.Has(FeatureISR) {
			t.Error("a stack that rolled back and holds nothing reads as an enabled feature; every deploy that needs it is waved through")
		}
	})

	t.Run("a rolled-back stack is replaced, not updated into a wall", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})
		apis := apisOf(cfn, ssmc, iamc, preloadedStore())
		holdNothing(t)

		req := Request{Features: []string{FeatureISR}}
		if err := Run(context.Background(), apis, ClassProduction, req, nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		cfn.wedge(isrStack(ClassProduction))

		if err := Run(context.Background(), apis, ClassProduction, req, nil, nil); err != nil {
			t.Fatalf("re-run over a wedged stack: %v", err)
		}
		if !slices.Contains(cfn.deleted, isrStack(ClassProduction)) {
			t.Errorf("deleted %v, want the wedged stack replaced rather than updated", cfn.deleted)
		}
		deployed, err := CheckDeployed(context.Background(), cfn)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		if !deployed.Features.Has(FeatureISR) {
			t.Error("the re-run left the feature missing")
		}
	})

	t.Run("a wedged stack left out of the set is still taken away", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})
		apis := apisOf(cfn, ssmc, iamc, preloadedStore())
		holdNothing(t)

		if err := Run(context.Background(), apis, ClassProduction, Request{Features: []string{FeatureISR}}, nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		cfn.wedge(isrStack(ClassProduction))

		if err := Run(context.Background(), apis, ClassProduction, Request{Remove: []string{FeatureISR}}, nil, nil); err != nil {
			t.Fatalf("dropping a wedged stack: %v", err)
		}
		if slices.Contains(cfn.stacks(), isrStack(ClassProduction)) {
			t.Errorf("stacks left = %v, want the wedged stack gone rather than orphaned", cfn.stacks())
		}
	})

	t.Run("a queue name still held is waited out, not surfaced", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})
		held := holdNothing(t)
		cfn.holdName(isrStack(ClassProduction), 2)

		if err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), ClassProduction,
			Request{Features: []string{FeatureISR}}, nil, nil); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(*held) != 2 {
			t.Errorf("held off %d times, want one wait per re-create of the held name", len(*held))
		}
		for i := 1; i < len(*held); i++ {
			if (*held)[i] <= (*held)[i-1] {
				t.Errorf("waits %v do not grow; a fixed name held for a minute needs backoff", *held)
			}
		}
	})

	t.Run("a stack that fails for any other reason is not retried", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		frontedBy(t, &fakeEdge{kind: "cloudflare"})
		holdNothing(t)
		cfn.holdName(isrStack(ClassProduction), 1)
		cfn.reason(isrStack(ClassProduction), "Resource handler returned message: Access denied")

		err := Run(context.Background(), apisOf(cfn, ssmc, iamc, preloadedStore()), ClassProduction,
			Request{Features: []string{FeatureISR}}, nil, nil)
		if err == nil {
			t.Fatal("retried a failure that will never succeed on retry")
		}
	})
}

func holdNothing(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	var mu sync.Mutex
	previous := holdBefore
	holdBefore = func(context.Context, time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		waits = append(waits, nameHeldDelay(len(waits)))
		return nil
	}
	t.Cleanup(func() { holdBefore = previous })
	return &waits
}
