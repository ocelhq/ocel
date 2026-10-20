package imageoptimizer

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
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/lambdapart"
)

const (
	Part    = "image-optimizer"
	Summary = "the one shared image transform every front calls"

	ComponentTagKey   = "ocel:component"
	ComponentTagValue = "image-optimizer"

	runtime          = "nodejs24.x"
	architecture     = "arm64"
	handler          = "index.handler"
	memoryMB         = 1769
	timeoutSeconds   = 20
	threadpoolSize   = 4
	retentionDays    = 14
	bucketEnvVar     = "OCEL_EDGE_BUCKET"
	threadpoolEnvVar = "UV_THREADPOOL_SIZE"
	policyName       = "image-optimizer-read"

	lambdaPrincipal = "lambda.amazonaws.com"

	kindRole     = "AWS::IAM::Role"
	kindLogGroup = "AWS::Logs::LogGroup"
	kindFunction = "AWS::Lambda::Function"
	kindURL      = "AWS::Lambda::Url"
)

type Spec struct {
	Namespace     string
	Tier          environment.Tier
	Region        string
	Boundary      string
	RolePath      string
	EdgeBucket    string
	EdgeBucketARN string

	UpdatesTrust bool
}

type Names struct {
	Function string
	Role     string
	LogGroup string
}

func NamesFor(namespace string, tier environment.Tier) Names {
	function := namespace + "-optimizer"
	if tier == environment.TierPreview {
		function += "-preview"
	}
	return Names{Function: function, Role: function, LogGroup: "/aws/lambda/" + function}
}

func FunctionARN(region, account, namespace string, tier environment.Tier) string {
	return "arn:aws:lambda:" + region + ":" + account + ":function:" + NamesFor(namespace, tier).Function
}

type LambdaAPI interface {
	lambdapart.LambdaAPI
	CreateFunctionUrlConfig(ctx context.Context, in *lambda.CreateFunctionUrlConfigInput, optFns ...func(*lambda.Options)) (*lambda.CreateFunctionUrlConfigOutput, error)
	UpdateFunctionUrlConfig(ctx context.Context, in *lambda.UpdateFunctionUrlConfigInput, optFns ...func(*lambda.Options)) (*lambda.UpdateFunctionUrlConfigOutput, error)
}

type Clients struct {
	IAM    lambdapart.IAMAPI
	Lambda LambdaAPI
	Logs   lambdapart.LogsAPI
}

func NewClients(cfg aws.Config) Clients {
	return Clients{IAM: iam.NewFromConfig(cfg), Lambda: lambda.NewFromConfig(cfg), Logs: cloudwatchlogs.NewFromConfig(cfg)}
}

func (c Clients) functions() lambdapart.Clients {
	return lambdapart.Clients{IAM: c.IAM, Lambda: c.Lambda, Logs: c.Logs}
}

func Digest(spec Spec) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		payloads.ImageOptimizer().SHA256, spec.Namespace, string(spec.Tier), spec.Region, spec.Boundary, spec.RolePath, spec.EdgeBucket, spec.EdgeBucketARN,
	}, "\n")))
	return hex.EncodeToString(sum[:])
}

func Ensure(ctx context.Context, c Clients, spec Spec) ([]provider.PartResource, error) {
	names := NamesFor(spec.Namespace, spec.Tier)
	account, err := accountOf(spec.Boundary)
	if err != nil {
		return nil, err
	}
	policy, err := lambdapart.Document(
		map[string]any{"Effect": "Allow", "Action": "s3:GetObject", "Resource": []string{
			spec.EdgeBucketARN + "/" + string(naming.ObjectAssets) + "/*",
			spec.EdgeBucketARN + "/" + string(naming.ObjectImageConfig) + "/*",
		}},
		map[string]any{"Effect": "Allow", "Action": []string{"logs:CreateLogStream", "logs:PutLogEvents"},
			"Resource": "arn:aws:logs:" + spec.Region + ":" + account + ":log-group:" + names.LogGroup + ":*"},
	)
	if err != nil {
		return nil, err
	}
	behind := lambdapart.EnsureRole(ctx, c.IAM, lambdapart.Role{
		Name:         names.Role,
		Path:         spec.RolePath,
		Boundary:     spec.Boundary,
		Description:  "Ocel: the role the " + string(spec.Tier) + " image optimizer runs as, reading the edge bucket's assets and image config",
		Service:      lambdaPrincipal,
		PolicyName:   policyName,
		Policy:       policy,
		Tags:         naming.OcelTags(spec.Namespace, spec.Tier),
		UpdatesTrust: spec.UpdatesTrust,
	})
	if behind != nil && !errors.Is(behind, stackrecords.ErrPartBehind) {
		return nil, behind
	}
	functionTags := naming.OcelTags(spec.Namespace, spec.Tier)
	functionTags[ComponentTagKey] = ComponentTagValue
	if err := lambdapart.Ensure(ctx, c.functions(), lambdapart.Function{
		Name:           names.Function,
		Description:    "Ocel image optimizer - one shared function transforming /_next/image for every app in the " + string(spec.Tier) + " tier, invoked by the edge.",
		Runtime:        runtime,
		Architecture:   architecture,
		Handler:        handler,
		MemoryMB:       memoryMB,
		TimeoutSeconds: timeoutSeconds,
		Role:           "arn:aws:iam::" + account + ":role" + spec.RolePath + names.Role,
		Code:           payloads.ImageOptimizer().Bytes,
		Variables:      map[string]string{bucketEnvVar: spec.EdgeBucket, threadpoolEnvVar: strconv.Itoa(threadpoolSize)},
		LogGroup:       names.LogGroup,
		RetentionDays:  retentionDays,
		Tags:           functionTags,
	}); err != nil {
		return nil, err
	}
	url, err := ensureURL(ctx, c.Lambda, names.Function)
	if err != nil {
		return nil, err
	}
	return []provider.PartResource{
		{Kind: kindRole, ID: names.Role},
		{Kind: kindLogGroup, ID: names.LogGroup},
		{Kind: kindFunction, ID: names.Function},
		{Kind: kindURL, ID: url},
	}, behind
}

func accountOf(boundary string) (string, error) {
	fields := strings.Split(boundary, ":")
	if len(fields) < 6 || fields[4] == "" {
		return "", fmt.Errorf("the app boundary %q is no ARN, so the image optimizer's role cannot name the account it runs in", boundary)
	}
	return fields[4], nil
}

func ensureURL(ctx context.Context, c LambdaAPI, function string) (string, error) {
	var url string
	err := lambdapart.Retrying(ctx, lambdapart.Updating, func() error {
		created, err := c.CreateFunctionUrlConfig(ctx, &lambda.CreateFunctionUrlConfigInput{
			FunctionName: aws.String(function),
			AuthType:     lambdatypes.FunctionUrlAuthTypeAwsIam,
			InvokeMode:   lambdatypes.InvokeModeResponseStream,
		})
		if err == nil {
			url = aws.ToString(created.FunctionUrl)
			return nil
		}
		var conflict *lambdatypes.ResourceConflictException
		if !errors.As(err, &conflict) {
			return err
		}
		updated, err := c.UpdateFunctionUrlConfig(ctx, &lambda.UpdateFunctionUrlConfigInput{
			FunctionName: aws.String(function),
			AuthType:     lambdatypes.FunctionUrlAuthTypeAwsIam,
			InvokeMode:   lambdatypes.InvokeModeResponseStream,
		})
		if err == nil {
			url = aws.ToString(updated.FunctionUrl)
		}
		return err
	})
	if err != nil {
		return "", fmt.Errorf("give function %s its IAM-authenticated url: %w", function, err)
	}
	return url, nil
}

func URLOf(resources []provider.PartResource) string {
	for _, resource := range resources {
		if resource.Kind == kindURL {
			return resource.ID
		}
	}
	return ""
}

func Remove(ctx context.Context, c Clients, namespace string, tier environment.Tier) error {
	names := NamesFor(namespace, tier)
	if err := lambdapart.Remove(ctx, c.functions(), names.Function, names.LogGroup); err != nil {
		return err
	}
	return lambdapart.RemoveRole(ctx, c.IAM, names.Role)
}

func Shape(namespace string, tier environment.Tier) []pricing.Shaped {
	names := NamesFor(namespace, tier)
	return []pricing.Shaped{
		{Name: names.Function, Type: "aws_lambda_function", Properties: map[string]any{
			"runtime": runtime, "memory_size": memoryMB, "timeout": timeoutSeconds, "architectures": []any{architecture},
		}},
		{Name: names.Function + "-url", Type: "aws_lambda_function_url", Properties: map[string]any{}},
		{Name: names.LogGroup, Type: "aws_cloudwatch_log_group", Properties: map[string]any{"retention_in_days": retentionDays}},
	}
}
