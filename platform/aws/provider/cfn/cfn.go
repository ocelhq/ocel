package cfn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	mathrand "math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	TagDigest         = "ocel:digest"
	TagBootstrappedBy = "ocel:bootstrapped-by"
	TagNamespace      = "ocel:namespace"
)

const (
	ChangeSetAttempts = 40
	changeSetBase     = 2 * time.Second
	changeSetCeiling  = 15 * time.Second
	changeSetJitter   = 0.2
)

const (
	NameHeldAttempts = 4
	nameHeldBase     = 20 * time.Second
	nameHeldCeiling  = 90 * time.Second
	nameHeldJitter   = 0.2

	HeldQueueName = "QueueDeletedRecently"
)

const (
	CreateWaitTimeout = 20 * time.Minute
	UpdateWaitTimeout = 20 * time.Minute
	DeleteWaitTimeout = 30 * time.Minute
	stackWaitMinDelay = 5 * time.Second
	stackWaitMaxDelay = 20 * time.Second
)

const failureEventPages = 5

const (
	deleteBatchSize     = 1000
	refusedObjectsShown = 10
	discardGrace        = 20 * time.Second
)

type ChangeSetNamer interface {
	ChangeSetNameFor(stackName string) string
}

type ChangeReview func(stackName string, changes []cfntypes.ResourceChange) error

var HoldBefore = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func tagValue(tags []cfntypes.Tag, key string) string {
	for _, tag := range tags {
		if aws.ToString(tag.Key) == key {
			return aws.ToString(tag.Value)
		}
	}
	return ""
}

func IsValidationErrorContaining(err error, substr string) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.ErrorCode() == "ValidationError" && strings.Contains(apiErr.ErrorMessage(), substr)
}

type Describer interface {
	DescribeStacks(ctx context.Context, in *cloudformation.DescribeStacksInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error)
}

type API interface {
	Describer
	CreateStack(ctx context.Context, in *cloudformation.CreateStackInput, optFns ...func(*cloudformation.Options)) (*cloudformation.CreateStackOutput, error)
	UpdateStack(ctx context.Context, in *cloudformation.UpdateStackInput, optFns ...func(*cloudformation.Options)) (*cloudformation.UpdateStackOutput, error)
	DeleteStack(ctx context.Context, in *cloudformation.DeleteStackInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DeleteStackOutput, error)
	DescribeStackEvents(ctx context.Context, in *cloudformation.DescribeStackEventsInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DescribeStackEventsOutput, error)
	ListStackResources(ctx context.Context, in *cloudformation.ListStackResourcesInput, optFns ...func(*cloudformation.Options)) (*cloudformation.ListStackResourcesOutput, error)
	CreateChangeSet(ctx context.Context, in *cloudformation.CreateChangeSetInput, optFns ...func(*cloudformation.Options)) (*cloudformation.CreateChangeSetOutput, error)
	DescribeChangeSet(ctx context.Context, in *cloudformation.DescribeChangeSetInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DescribeChangeSetOutput, error)
	ExecuteChangeSet(ctx context.Context, in *cloudformation.ExecuteChangeSetInput, optFns ...func(*cloudformation.Options)) (*cloudformation.ExecuteChangeSetOutput, error)
	DeleteChangeSet(ctx context.Context, in *cloudformation.DeleteChangeSetInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DeleteChangeSetOutput, error)
}

type TeardownAPI interface {
	Describer
	DeleteStack(ctx context.Context, in *cloudformation.DeleteStackInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DeleteStackOutput, error)
}

type BucketEmptierAPI interface {
	ListObjectVersions(ctx context.Context, in *s3.ListObjectVersionsInput, optFns ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error)
	DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

func DescribeStack(ctx context.Context, api Describer, stackName string) (*cfntypes.Stack, error) {
	out, err := api.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stackName)})
	if err != nil {
		if isStackNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("describe %s stack: %w", stackName, err)
	}
	if len(out.Stacks) == 0 {
		return nil, nil
	}
	return &out.Stacks[0], nil
}

func OutputsOf(stack *cfntypes.Stack) map[string]string {
	values := make(map[string]string, len(stack.Outputs))
	for _, o := range stack.Outputs {
		values[aws.ToString(o.OutputKey)] = aws.ToString(o.OutputValue)
	}
	return values
}

func StackOutputs(ctx context.Context, api Describer, stackName string) (map[string]string, error) {
	stack, err := DescribeStack(ctx, api, stackName)
	if err != nil || stack == nil {
		return nil, err
	}
	return OutputsOf(stack), nil
}

func Unusable(status cfntypes.StackStatus) bool {
	switch status {
	case cfntypes.StackStatusCreateFailed, cfntypes.StackStatusRollbackComplete, cfntypes.StackStatusRollbackFailed:
		return true
	default:
		return false
	}
}

func Upsert(ctx context.Context, cfn API, namer ChangeSetNamer, stackName, template string, params []cfntypes.Parameter, capabilities []cfntypes.Capability, tags []cfntypes.Tag, review ChangeReview) error {
	stack, err := DescribeStack(ctx, cfn, stackName)
	if err != nil {
		return err
	}
	if stack != nil && Unusable(stack.StackStatus) {
		if err := Delete(ctx, cfn, stackName); err != nil {
			return err
		}
		stack = nil
	}
	if stack == nil {
		return Create(ctx, cfn, stackName, template, params, capabilities, tags)
	}
	return Update(ctx, cfn, namer, stackName, template, params, capabilities, tags, review)
}

func Update(ctx context.Context, cfn API, namer ChangeSetNamer, stackName, template string, params []cfntypes.Parameter, capabilities []cfntypes.Capability, tags []cfntypes.Tag, review ChangeReview) error {
	id, changes, err := Plan(ctx, cfn, namer, stackName, template, params, capabilities, tags)
	if err != nil {
		return err
	}
	if id == "" {
		return Restamp(ctx, cfn, stackName, params, capabilities, tags)
	}

	executed := false
	defer func() {
		if !executed {
			DiscardChangeSet(ctx, cfn, id)
		}
	}()

	if review != nil {
		if err := review(stackName, changes); err != nil {
			return err
		}
	}
	if _, err := cfn.ExecuteChangeSet(ctx, &cloudformation.ExecuteChangeSetInput{ChangeSetName: aws.String(id)}); err != nil {
		return fmt.Errorf("update %s stack: %w", stackName, err)
	}
	executed = true

	w := cloudformation.NewStackUpdateCompleteWaiter(cfn, UpdateCadence)
	if err := w.Wait(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stackName)}, UpdateWaitTimeout); err != nil {
		return waitFailure(ctx, cfn, stackName, "update", err)
	}
	return nil
}

func Restamp(ctx context.Context, cfn API, stackName string, params []cfntypes.Parameter, capabilities []cfntypes.Capability, tags []cfntypes.Tag) error {
	stack, err := DescribeStack(ctx, cfn, stackName)
	if err != nil || stack == nil {
		return err
	}
	if SameTags(stack.Tags, tags) || onlyDevWriterMoved(stack.Tags, tags) {
		return nil
	}
	if _, err := cfn.UpdateStack(ctx, &cloudformation.UpdateStackInput{
		StackName:           aws.String(stackName),
		UsePreviousTemplate: aws.Bool(true),
		Parameters:          params,
		Capabilities:        capabilities,
		Tags:                tags,
	}); err != nil {
		if changeSetEmpty(err.Error()) {
			return nil
		}
		return fmt.Errorf("restamp %s: %w", stackName, err)
	}
	w := cloudformation.NewStackUpdateCompleteWaiter(cfn, UpdateCadence)
	if err := w.Wait(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stackName)}, UpdateWaitTimeout); err != nil {
		return waitFailure(ctx, cfn, stackName, "restamp", err)
	}
	return nil
}

func SameTags(have, want []cfntypes.Tag) bool {
	if len(have) != len(want) {
		return false
	}
	standing := make(map[string]string, len(have))
	for _, tag := range have {
		standing[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	for _, tag := range want {
		value, ok := standing[aws.ToString(tag.Key)]
		if !ok || value != aws.ToString(tag.Value) {
			return false
		}
	}
	return true
}

func Plan(ctx context.Context, cfn API, namer ChangeSetNamer, stackName, template string, params []cfntypes.Parameter, capabilities []cfntypes.Capability, tags []cfntypes.Tag) (string, []cfntypes.ResourceChange, error) {
	out, err := cfn.CreateChangeSet(ctx, &cloudformation.CreateChangeSetInput{
		StackName:     aws.String(stackName),
		ChangeSetName: aws.String(namer.ChangeSetNameFor(stackName)),
		ChangeSetType: cfntypes.ChangeSetTypeUpdate,
		TemplateBody:  aws.String(template),
		Parameters:    params,
		Capabilities:  capabilities,
		Tags:          tags,
	})
	if err != nil {
		return "", nil, fmt.Errorf("plan the %s update: %w", stackName, err)
	}
	id := aws.ToString(out.Id)

	changes, reason, err := AwaitChangeSet(ctx, cfn, id)
	if err != nil {
		DiscardChangeSet(ctx, cfn, id)
		return "", nil, fmt.Errorf("plan the %s update: %w", stackName, err)
	}
	if reason != "" {
		DiscardChangeSet(ctx, cfn, id)
		if changeSetEmpty(reason) {
			return "", nil, nil
		}
		return "", nil, fmt.Errorf("plan the %s update: %s", stackName, reason)
	}
	return id, changes, nil
}

func AwaitChangeSet(ctx context.Context, cfn API, id string) ([]cfntypes.ResourceChange, string, error) {
	for attempt := 0; ; attempt++ {
		var changes []cfntypes.ResourceChange
		var token *string
		for {
			out, err := cfn.DescribeChangeSet(ctx, &cloudformation.DescribeChangeSetInput{
				ChangeSetName: aws.String(id),
				NextToken:     token,
			})
			if err != nil {
				return nil, "", err
			}
			for _, c := range out.Changes {
				if c.ResourceChange != nil {
					changes = append(changes, *c.ResourceChange)
				}
			}
			if out.NextToken == nil {
				switch out.Status {
				case cfntypes.ChangeSetStatusFailed:
					return nil, changeSetReason(out), nil
				case cfntypes.ChangeSetStatusCreateComplete:
					return changes, "", nil
				case cfntypes.ChangeSetStatusDeletePending,
					cfntypes.ChangeSetStatusDeleteInProgress,
					cfntypes.ChangeSetStatusDeleteComplete,
					cfntypes.ChangeSetStatusDeleteFailed:
					return nil, "", fmt.Errorf("change set %s is %s: something else took it away while this run was planning against it", id, out.Status)
				}
				break
			}
			token = out.NextToken
		}
		if attempt+1 >= ChangeSetAttempts {
			return nil, "", fmt.Errorf("change set %s was still being built after %d looks", id, ChangeSetAttempts)
		}
		if err := HoldBefore(ctx, ChangeSetDelay(attempt)); err != nil {
			return nil, "", err
		}
	}
}

func changeSetReason(out *cloudformation.DescribeChangeSetOutput) string {
	if reason := aws.ToString(out.StatusReason); reason != "" {
		return reason
	}
	return string(cfntypes.ChangeSetStatusFailed)
}

func changeSetEmpty(reason string) bool {
	return strings.Contains(reason, "didn't contain changes") ||
		strings.Contains(reason, "No updates are to be performed")
}

func ChangeSetDelay(attempt int) time.Duration {
	step := min(changeSetBase<<min(attempt, 3), changeSetCeiling)
	return step + time.Duration(mathrand.Float64()*changeSetJitter*float64(step))
}

func DiscardChangeSet(ctx context.Context, cfn API, id string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discardGrace)
	defer cancel()
	_, _ = cfn.DeleteChangeSet(ctx, &cloudformation.DeleteChangeSetInput{ChangeSetName: aws.String(id)})
}

func Create(ctx context.Context, cfn API, stackName, template string, params []cfntypes.Parameter, capabilities []cfntypes.Capability, tags []cfntypes.Tag) error {
	for attempt := 0; ; attempt++ {
		err := createOnce(ctx, cfn, stackName, template, params, capabilities, tags)
		if err == nil {
			return nil
		}
		if attempt+1 >= NameHeldAttempts || !nameStillHeld(ctx, cfn, stackName) {
			return err
		}
		if err := Delete(ctx, cfn, stackName); err != nil {
			return err
		}
		if err := HoldBefore(ctx, NameHeldDelay(attempt)); err != nil {
			return err
		}
	}
}

func createOnce(ctx context.Context, cfn API, stackName, template string, params []cfntypes.Parameter, capabilities []cfntypes.Capability, tags []cfntypes.Tag) error {
	if _, err := cfn.CreateStack(ctx, &cloudformation.CreateStackInput{
		StackName:    aws.String(stackName),
		TemplateBody: aws.String(template),
		Parameters:   params,
		Capabilities: capabilities,
		Tags:         tags,
	}); err != nil {
		return fmt.Errorf("create %s stack: %w", stackName, err)
	}
	w := cloudformation.NewStackCreateCompleteWaiter(cfn, CreateCadence)
	if err := w.Wait(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stackName)}, CreateWaitTimeout); err != nil {
		return waitFailure(ctx, cfn, stackName, "create", err)
	}
	return nil
}

func waitFailure(ctx context.Context, cfn API, stackName, action string, err error) error {
	if reason := firstFailure(ctx, cfn, stackName); reason != "" {
		return fmt.Errorf("wait for %s %s: %w: %s", stackName, action, err, reason)
	}
	return fmt.Errorf("wait for %s %s: %w", stackName, action, err)
}

func firstFailure(ctx context.Context, cfn API, stackName string) string {
	held, found := failureOfThisOperation(ctx, cfn, stackName)
	if !found {
		return ""
	}
	if embedded := embeddedStackOf(held); embedded != "" {
		if deeper, ok := failureOfThisOperation(ctx, cfn, embedded); ok {
			return describeFailure(deeper)
		}
	}
	return describeFailure(held)
}

func failureOfThisOperation(ctx context.Context, cfn API, stackName string) (cfntypes.StackEvent, bool) {
	var (
		token  *string
		failed cfntypes.StackEvent
		found  bool
	)
	for range failureEventPages {
		out, err := cfn.DescribeStackEvents(ctx, &cloudformation.DescribeStackEventsInput{
			StackName: aws.String(stackName),
			NextToken: token,
		})
		if err != nil {
			return failed, found
		}
		for _, held := range out.StackEvents {
			if beganTheOperation(held) {
				return failed, found
			}
			if failedEvent(held) {
				failed, found = held, true
			}
		}
		if token = out.NextToken; aws.ToString(token) == "" {
			return failed, found
		}
	}
	return failed, found
}

var operationStarts = []cfntypes.ResourceStatus{
	cfntypes.ResourceStatusCreateInProgress,
	cfntypes.ResourceStatusUpdateInProgress,
	cfntypes.ResourceStatusImportInProgress,
}

func beganTheOperation(held cfntypes.StackEvent) bool {
	id := aws.ToString(held.StackId)
	return id != "" && aws.ToString(held.PhysicalResourceId) == id &&
		slices.Contains(operationStarts, held.ResourceStatus)
}

const stackResourceType = "AWS::CloudFormation::Stack"

func embeddedStackOf(held cfntypes.StackEvent) string {
	if aws.ToString(held.ResourceType) != stackResourceType {
		return ""
	}
	if id := aws.ToString(held.PhysicalResourceId); id != aws.ToString(held.StackId) {
		return id
	}
	return ""
}

var cancellations = []string{
	"Resource creation cancelled",
	"Resource update cancelled",
	"Resource deletion cancelled",
}

func failedEvent(held cfntypes.StackEvent) bool {
	reason := strings.TrimSuffix(strings.TrimSpace(aws.ToString(held.ResourceStatusReason)), ".")
	return strings.HasSuffix(string(held.ResourceStatus), "_FAILED") &&
		reason != "" && !slices.Contains(cancellations, reason)
}

func describeFailure(held cfntypes.StackEvent) string {
	return fmt.Sprintf("%s %s %s: %s",
		aws.ToString(held.LogicalResourceId), aws.ToString(held.ResourceType),
		held.ResourceStatus, aws.ToString(held.ResourceStatusReason))
}

func NameHeldDelay(attempt int) time.Duration {
	step := min(nameHeldBase<<attempt, nameHeldCeiling)
	return step + time.Duration(mathrand.Float64()*nameHeldJitter*float64(step))
}

func nameStillHeld(ctx context.Context, cfn API, stackName string) bool {
	out, err := cfn.DescribeStackEvents(ctx, &cloudformation.DescribeStackEventsInput{StackName: aws.String(stackName)})
	if err != nil {
		return false
	}
	for _, event := range out.StackEvents {
		if strings.Contains(aws.ToString(event.ResourceStatusReason), HeldQueueName) {
			return true
		}
	}
	return false
}

func isStackNotFound(err error) bool {
	return IsValidationErrorContaining(err, "does not exist")
}

func CreateCadence(o *cloudformation.StackCreateCompleteWaiterOptions) {
	o.MinDelay, o.MaxDelay = stackWaitMinDelay, stackWaitMaxDelay
}

func UpdateCadence(o *cloudformation.StackUpdateCompleteWaiterOptions) {
	o.MinDelay, o.MaxDelay = stackWaitMinDelay, stackWaitMaxDelay
}

func DeleteCadence(o *cloudformation.StackDeleteCompleteWaiterOptions) {
	o.MinDelay, o.MaxDelay = stackWaitMinDelay, stackWaitMaxDelay
}

func Delete(ctx context.Context, cfn TeardownAPI, stackName string) error {
	if _, err := cfn.DeleteStack(ctx, &cloudformation.DeleteStackInput{StackName: aws.String(stackName)}); err != nil {
		return fmt.Errorf("delete %s stack: %w", stackName, err)
	}
	w := cloudformation.NewStackDeleteCompleteWaiter(cfn, DeleteCadence)
	if err := w.Wait(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stackName)}, DeleteWaitTimeout); err != nil {
		return fmt.Errorf("wait for %s delete: %w", stackName, err)
	}
	return nil
}

func EmptyBucket(ctx context.Context, api BucketEmptierAPI, bucket string) error {
	var keyMarker, versionMarker *string
	for {
		out, err := api.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
			Bucket:          aws.String(bucket),
			KeyMarker:       keyMarker,
			VersionIdMarker: versionMarker,
			MaxKeys:         aws.Int32(deleteBatchSize),
		})
		if err != nil {
			if bucketGone(err) {
				return nil
			}
			return fmt.Errorf("list %s: %w", bucket, err)
		}
		ids := make([]s3types.ObjectIdentifier, 0, len(out.Versions)+len(out.DeleteMarkers))
		for _, v := range out.Versions {
			ids = append(ids, s3types.ObjectIdentifier{Key: v.Key, VersionId: v.VersionId})
		}
		for _, m := range out.DeleteMarkers {
			ids = append(ids, s3types.ObjectIdentifier{Key: m.Key, VersionId: m.VersionId})
		}
		if len(ids) > 0 {
			deleted, err := api.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(bucket),
				Delete: &s3types.Delete{Objects: ids, Quiet: aws.Bool(true)},
			})
			if err != nil {
				if bucketGone(err) {
					return nil
				}
				return fmt.Errorf("delete objects in %s: %w", bucket, err)
			}
			if err := refusedObjects(bucket, deleted.Errors); err != nil {
				return err
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			return nil
		}
		keyMarker, versionMarker = out.NextKeyMarker, out.NextVersionIdMarker
	}
}

func bucketGone(err error) bool {
	var missing *s3types.NoSuchBucket
	if errors.As(err, &missing) {
		return true
	}
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchBucket" || apiErr.ErrorCode() == "NotFound")
}

func refusedObjects(bucket string, refused []s3types.Error) error {
	if len(refused) == 0 {
		return nil
	}
	shown := refused
	if len(shown) > refusedObjectsShown {
		shown = shown[:refusedObjectsShown]
	}
	reasons := make([]string, 0, len(shown))
	for _, e := range shown {
		reasons = append(reasons, fmt.Sprintf("%s: %s (%s)", aws.ToString(e.Key), aws.ToString(e.Message), aws.ToString(e.Code)))
	}
	more := ""
	if len(refused) > len(shown) {
		more = fmt.Sprintf(" (and %d more)", len(refused)-len(shown))
	}
	return fmt.Errorf("empty %s: S3 refused %d object(s): %s%s", bucket, len(refused), strings.Join(reasons, "; "), more)
}

func TemplateDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func onlyDevWriterMoved(have, want []cfntypes.Tag) bool {
	from := providerkit.WrittenBy(tagValue(have, TagBootstrappedBy))
	to := providerkit.WrittenBy(tagValue(want, TagBootstrappedBy))
	if from == to || !from.Development() || !to.Development() {
		return false
	}
	return SameTags(WithoutTag(have, TagBootstrappedBy), WithoutTag(want, TagBootstrappedBy))
}

func WithoutTag(tags []cfntypes.Tag, key string) []cfntypes.Tag {
	out := make([]cfntypes.Tag, 0, len(tags))
	for _, tag := range tags {
		if aws.ToString(tag.Key) != key {
			out = append(out, tag)
		}
	}
	return out
}
