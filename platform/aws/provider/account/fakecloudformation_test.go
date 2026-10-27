package account

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	smithy "github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

type fakeCFN struct {
	mu           sync.Mutex
	templates    map[string]string
	params       map[string][]cfntypes.Parameter
	statuses     map[string]cfntypes.StackStatus
	outputs      map[string]map[string]string
	tags         map[string][]cfntypes.Tag
	reasons      map[string]string
	blockedNames map[string]int
	planned      map[string][]cfntypes.ResourceChange
	changeSets   map[string]*fakeChangeSet
	users        *fakeIAM
	applied      []string
	deleted      []string
	events       []string
	planning     []string
	executed     []string
	discarded    []string
	creates      int
	updates      int
	restamps     int
	noops        int
}

func newFakeCFN() *fakeCFN {
	return &fakeCFN{
		templates:    map[string]string{},
		params:       map[string][]cfntypes.Parameter{},
		statuses:     map[string]cfntypes.StackStatus{},
		outputs:      map[string]map[string]string{},
		tags:         map[string][]cfntypes.Tag{},
		reasons:      map[string]string{},
		blockedNames: map[string]int{},
		planned:      map[string][]cfntypes.ResourceChange{},
		changeSets:   map[string]*fakeChangeSet{},
	}
}

func (f *fakeCFN) blockName(stackName string, creates int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blockedNames[stackName] = creates
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
	if f.blockedNames[name] > 0 {
		f.blockedNames[name]--
		f.statuses[name] = cfntypes.StackStatusRollbackComplete
		if _, ok := f.reasons[name]; !ok {
			f.reasons[name] = "The following resource(s) failed to create: [TagInvalidatorDeadLetterQueue]. " + cfn.QueueDeletedRecently
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
	if cfn.SameTags(f.tags[name], in.Tags) {
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
	for _, resource := range declaredResources(body) {
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

const behindTemplate = `{"AWSTemplateFormatVersion": "2010-09-09", "Resources": {}}`

func (f *fakeCFN) fallBehind(stackName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.templates[stackName] = behindTemplate
	f.tags[stackName] = stampTags(defaultNamespace, environment.TierProduction, Stamp{Digest: cfn.TemplateDigest(behindTemplate), WrittenBy: "1.0.0"})
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

func (f *fakeCFN) changeSetsPlanned() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.planning)
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
		return nil, validationError{msg: "Stack " + name + " is DELETE_FAILED: cannot delete IAM user " + user + " while it has an access key created outside CloudFormation"}
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

func (f *fakeCFN) stamped(stackName string, tags []cfntypes.Tag) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tags[stackName] = tags
}

func declaredUser(template string) (string, bool) {
	var tmpl struct {
		Resources map[string]struct {
			Type       string `json:"Type"`
			Properties struct {
				UserName string `json:"UserName"`
			} `json:"Properties"`
		} `json:"Resources"`
	}
	if json.Unmarshal([]byte(template), &tmpl) != nil {
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
		Resources map[string]struct {
			Type       string `json:"Type"`
			Properties struct {
				BucketName string `json:"BucketName"`
			} `json:"Properties"`
		} `json:"Resources"`
		Outputs map[string]struct {
			Value json.RawMessage `json:"Value"`
		} `json:"Outputs"`
	}
	if err := json.Unmarshal([]byte(body), &tmpl); err != nil {
		panic("fake CloudFormation was handed a template it cannot parse: " + err.Error())
	}
	out := f.outputs[stackName]
	if out == nil {
		out = map[string]string{}
		f.outputs[stackName] = out
	}
	for key, o := range tmpl.Outputs {
		var literal string
		if json.Unmarshal(o.Value, &literal) == nil {
			out[key] = literal
			continue
		}
		var declared templateText
		if err := json.Unmarshal(o.Value, &declared); err != nil {
			panic("fake CloudFormation was handed an output it cannot read: " + err.Error())
		}
		if bucket, ok := tmpl.Resources[string(declared)]; ok && bucket.Type == "AWS::S3::Bucket" {
			out[key] = bucket.Properties.BucketName
			continue
		}
		out[key] = syntheticOutput(stackName, key, fakeAccount.Replace(string(declared)))
	}
}

var fakeAccount = strings.NewReplacer("${AWS::Partition}", "aws", "${AWS::AccountId}", "111122223333", "${AWS::Region}", "us-east-1")

func syntheticOutput(stackName, key, declared string) string {
	switch key {
	case outputInfraTier:
		return declared
	case outputStateTable:
		return "ocel-statetable-test"
	case outputVariablesKeyBrought:
		return declared
	}
	if strings.HasPrefix(declared, "arn:") {
		return declared
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

func (f *fakeCFN) template(stackName string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.templates[stackName]
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

func (f *fakeCFN) firstApplied(stackName string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Index(f.applied, stackName)
}
