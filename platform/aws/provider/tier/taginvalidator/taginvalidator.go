package taginvalidator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/lambdapart"
)

const (
	Part    = "tag-invalidator"
	Summary = "the function that clears a revalidated page's stale copies from the fronts that cached it"

	runtime         = "nodejs24.x"
	architecture    = "arm64"
	handler         = "index.handler"
	memoryMB        = 512
	timeoutSeconds  = 60
	retentionDays   = 14
	batchSize       = 100
	retries         = 5
	dlqRetentionSec = 1209600

	stateTableEnvVar = "OCEL_STATE_TABLE"
	seedParamEnvVar  = "OCEL_PURGE_SEED_PARAM"

	StreamFilter = `{"dynamodb":{"Keys":{"pk":{"S":[{"prefix":"PROJECT#"}]},"sk":{"S":["#META"]}}}}`

	invalidatorWord = "invalidator"

	kindQueue    = "AWS::SQS::Queue"
	kindLogGroup = "AWS::Logs::LogGroup"
	kindFunction = "AWS::Lambda::Function"
	kindMapping  = "AWS::Lambda::EventSourceMapping"
)

type Spec struct {
	Namespace  string
	Tier       environment.Tier
	Region     string
	Account    string
	Role       string
	StateTable string
	StreamARN  string
	SeedParam  string
}

type Names struct {
	Function string
	Role     string
	LogGroup string
	Queue    string
}

func NamesFor(namespace string, tier environment.Tier) Names {
	function := naming.OcelName(namespace, tier, invalidatorWord)
	return Names{
		Function: function,
		Role:     function,
		LogGroup: "/aws/lambda/" + function,
		Queue:    function,
	}
}

type LambdaAPI interface {
	lambdapart.LambdaAPI
	CreateEventSourceMapping(ctx context.Context, in *lambda.CreateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.CreateEventSourceMappingOutput, error)
	ListEventSourceMappings(ctx context.Context, in *lambda.ListEventSourceMappingsInput, optFns ...func(*lambda.Options)) (*lambda.ListEventSourceMappingsOutput, error)
	UpdateEventSourceMapping(ctx context.Context, in *lambda.UpdateEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.UpdateEventSourceMappingOutput, error)
	DeleteEventSourceMapping(ctx context.Context, in *lambda.DeleteEventSourceMappingInput, optFns ...func(*lambda.Options)) (*lambda.DeleteEventSourceMappingOutput, error)
}

type SQSAPI interface {
	CreateQueue(ctx context.Context, in *sqs.CreateQueueInput, optFns ...func(*sqs.Options)) (*sqs.CreateQueueOutput, error)
	SetQueueAttributes(ctx context.Context, in *sqs.SetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.SetQueueAttributesOutput, error)
	GetQueueUrl(ctx context.Context, in *sqs.GetQueueUrlInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error)
	DeleteQueue(ctx context.Context, in *sqs.DeleteQueueInput, optFns ...func(*sqs.Options)) (*sqs.DeleteQueueOutput, error)
}

type Clients struct {
	Lambda LambdaAPI
	Logs   lambdapart.LogsAPI
	SQS    SQSAPI
}

func NewClients(cfg aws.Config) Clients {
	return Clients{Lambda: lambda.NewFromConfig(cfg), Logs: cloudwatchlogs.NewFromConfig(cfg), SQS: sqs.NewFromConfig(cfg)}
}

func (c Clients) functions() lambdapart.Clients {
	return lambdapart.Clients{Lambda: c.Lambda, Logs: c.Logs}
}

func Digest(spec Spec) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		payloads.TagInvalidator().SHA256, spec.Namespace, string(spec.Tier), spec.Region, spec.Account, spec.Role, spec.StateTable, spec.StreamARN, spec.SeedParam, StreamFilter,
	}, "\n")))
	return hex.EncodeToString(sum[:])
}

func QueueARN(region, account, namespace string, tier environment.Tier) string {
	return "arn:aws:sqs:" + region + ":" + account + ":" + NamesFor(namespace, tier).Queue
}

func Ensure(ctx context.Context, c Clients, spec Spec) ([]provider.PhysicalResource, error) {
	names := NamesFor(spec.Namespace, spec.Tier)
	if err := ensureQueue(ctx, c.SQS, names.Queue, spec.Namespace, spec.Tier); err != nil {
		return nil, err
	}
	if err := lambdapart.Ensure(ctx, c.functions(), lambdapart.Function{
		Name:           names.Function,
		Description:    "Ocel tag invalidator - turns tag raises read off the " + string(spec.Tier) + " state table's stream into invalidations on the fronts that cached the page.",
		Runtime:        runtime,
		Architecture:   architecture,
		Handler:        handler,
		MemoryMB:       memoryMB,
		TimeoutSeconds: timeoutSeconds,
		Role:           spec.Role,
		Code:           payloads.TagInvalidator().Bytes,
		Variables: map[string]string{
			stateTableEnvVar:    spec.StateTable,
			awsports.TierEnvVar: string(spec.Tier),
			seedParamEnvVar:     spec.SeedParam,
		},
		LogGroup:      names.LogGroup,
		RetentionDays: retentionDays,
		Tags:          naming.OcelTags(spec.Namespace, spec.Tier),
	}); err != nil {
		return nil, err
	}
	mapping, err := ensureMapping(ctx, c.Lambda, names.Function, spec.StreamARN, QueueARN(spec.Region, spec.Account, spec.Namespace, spec.Tier))
	if err != nil {
		return nil, err
	}
	return []provider.PhysicalResource{
		{Kind: kindQueue, ID: names.Queue},
		{Kind: kindLogGroup, ID: names.LogGroup},
		{Kind: kindFunction, ID: names.Function},
		{Kind: kindMapping, ID: mapping},
	}, nil
}

func ensureQueue(ctx context.Context, c SQSAPI, name, namespace string, tier environment.Tier) error {
	attributes := map[string]string{
		string(sqstypes.QueueAttributeNameMessageRetentionPeriod): strconv.Itoa(dlqRetentionSec),
		string(sqstypes.QueueAttributeNameSqsManagedSseEnabled):   "true",
	}
	_, err := c.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name), Attributes: attributes, Tags: naming.OcelTags(namespace, tier)})
	var exists *sqstypes.QueueNameExists
	if !errors.As(err, &exists) {
		if err != nil {
			return fmt.Errorf("create queue %s: %w", name, err)
		}
		return nil
	}
	found, err := c.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
	if err != nil {
		return fmt.Errorf("find queue %s: %w", name, err)
	}
	if _, err := c.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{QueueUrl: found.QueueUrl, Attributes: attributes}); err != nil {
		return fmt.Errorf("set the attributes of queue %s: %w", name, err)
	}
	return nil
}

func mappingOf(ctx context.Context, c LambdaAPI, function, stream string) (string, error) {
	listed, err := c.ListEventSourceMappings(ctx, &lambda.ListEventSourceMappingsInput{
		FunctionName:   aws.String(function),
		EventSourceArn: aws.String(stream),
	})
	if err != nil {
		return "", fmt.Errorf("list the event source mappings of function %s: %w", function, err)
	}
	for _, mapping := range listed.EventSourceMappings {
		return aws.ToString(mapping.UUID), nil
	}
	return "", nil
}

func ensureMapping(ctx context.Context, c LambdaAPI, function, stream, queue string) (string, error) {
	filters := &lambdatypes.FilterCriteria{Filters: []lambdatypes.Filter{{Pattern: aws.String(StreamFilter)}}}
	failures := &lambdatypes.DestinationConfig{OnFailure: &lambdatypes.OnFailure{Destination: aws.String(queue)}}
	responses := []lambdatypes.FunctionResponseType{lambdatypes.FunctionResponseTypeReportBatchItemFailures}
	existing, err := mappingOf(ctx, c, function, stream)
	if err != nil {
		return "", err
	}
	if existing != "" {
		err := lambdapart.Retrying(ctx, lambdapart.Updating, func() error {
			_, err := c.UpdateEventSourceMapping(ctx, &lambda.UpdateEventSourceMappingInput{
				UUID:                           aws.String(existing),
				FunctionName:                   aws.String(function),
				BatchSize:                      aws.Int32(batchSize),
				MaximumBatchingWindowInSeconds: aws.Int32(0),
				MaximumRetryAttempts:           aws.Int32(retries),
				FunctionResponseTypes:          responses,
				DestinationConfig:              failures,
				FilterCriteria:                 filters,
			})
			return err
		})
		if err != nil {
			return "", fmt.Errorf("update the stream mapping of function %s: %w", function, err)
		}
		return existing, nil
	}
	var created *lambda.CreateEventSourceMappingOutput
	err = lambdapart.Retrying(ctx, unpermittedOrUpdating, func() error {
		created, err = c.CreateEventSourceMapping(ctx, &lambda.CreateEventSourceMappingInput{
			FunctionName:                   aws.String(function),
			EventSourceArn:                 aws.String(stream),
			StartingPosition:               lambdatypes.EventSourcePositionLatest,
			BatchSize:                      aws.Int32(batchSize),
			MaximumBatchingWindowInSeconds: aws.Int32(0),
			MaximumRetryAttempts:           aws.Int32(retries),
			FunctionResponseTypes:          responses,
			DestinationConfig:              failures,
			FilterCriteria:                 filters,
		})
		return err
	})
	if err != nil {
		return "", fmt.Errorf("map the state table's stream onto function %s: %w", function, err)
	}
	return aws.ToString(created.UUID), nil
}

func unpermitted(err error) bool {
	var invalid *lambdatypes.InvalidParameterValueException
	return errors.As(err, &invalid) && strings.Contains(strings.ToLower(err.Error()), "role")
}

func unpermittedOrUpdating(err error) bool { return unpermitted(err) || lambdapart.Updating(err) }

func Remove(ctx context.Context, c Clients, namespace string, tier environment.Tier) error {
	names := NamesFor(namespace, tier)
	listed, err := c.Lambda.ListEventSourceMappings(ctx, &lambda.ListEventSourceMappingsInput{FunctionName: aws.String(names.Function)})
	var missing *lambdatypes.ResourceNotFoundException
	switch {
	case errors.As(err, &missing):
	case err != nil:
		return fmt.Errorf("list the event source mappings of function %s: %w", names.Function, err)
	default:
		for _, mapping := range listed.EventSourceMappings {
			if _, err := c.Lambda.DeleteEventSourceMapping(ctx, &lambda.DeleteEventSourceMappingInput{UUID: mapping.UUID}); err != nil && !errors.As(err, &missing) {
				return fmt.Errorf("delete the stream mapping of function %s: %w", names.Function, err)
			}
		}
	}
	if err := lambdapart.Remove(ctx, c.functions(), names.Function, names.LogGroup); err != nil {
		return err
	}
	found, err := c.SQS.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(names.Queue)})
	var absent *sqstypes.QueueDoesNotExist
	if errors.As(err, &absent) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find queue %s: %w", names.Queue, err)
	}
	if _, err := c.SQS.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: found.QueueUrl}); err != nil && !errors.As(err, &absent) {
		return fmt.Errorf("delete queue %s: %w", names.Queue, err)
	}
	return nil
}

func Inventory(namespace string, tier environment.Tier) []pricing.Item {
	names := NamesFor(namespace, tier)
	return []pricing.Item{
		{Name: names.Queue, Type: "aws_sqs_queue", Properties: map[string]any{"fifo_queue": false}},
		{Name: names.Function, Type: "aws_lambda_function", Properties: map[string]any{
			"runtime": runtime, "memory_size": memoryMB, "timeout": timeoutSeconds, "architectures": []any{architecture},
		}},
		{Name: names.Function + "-stream", Type: "aws_lambda_event_source_mapping", Properties: map[string]any{}},
		{Name: names.LogGroup, Type: "aws_cloudwatch_log_group", Properties: map[string]any{"retention_in_days": retentionDays}},
	}
}
