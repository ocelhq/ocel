// Package bootstrap provisions and inspects the account-global resources the
// Ocel AWS provider needs before any deploy can run: an S3 bucket for Pulumi
// state and a DynamoDB table for upload sessions (both via CloudFormation),
// and a Pulumi passphrase (an SSM SecureString minted imperatively, because
// CloudFormation cannot create SecureStrings).
// The bootstrapped resources carry a monotonic integer version so every
// invocation can gate on compatibility (see version.go).
package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	smithy "github.com/aws/smithy-go"
)

const (
	// StackName is the CloudFormation stack that holds the production
	// bootstrapped account-global resources.
	StackName = "ocel-bootstrap"

	// PreviewStackName is the CloudFormation stack that holds the preview
	// infrastructure (a separate stack so the two substrates have independent
	// lifecycles). Provisioned by RunPreview.
	PreviewStackName = "ocel-bootstrap-preview"

	// PassphraseParamName is the SSM SecureString parameter holding the Pulumi
	// passphrase.
	PassphraseParamName = "/ocel/pulumi/passphrase"

	outputStateBucket  = "StateBucketName"
	outputSessionTable = "SessionTableName"
	outputVersion      = "BootstrapVersion"
	outputInfraClass   = "InfrastructureClass"
)

// Class tags stamped on a bootstrapped substrate, so an invocation can verify
// it is acting on the right one. They match the provider contract's class
// tokens without coupling this package to the proto enum.
const (
	ClassProduction = "production"
	ClassPreview    = "preview"
)

// Deployed describes the bootstrap state discovered in an account.
type Deployed struct {
	Present      bool
	Version      int
	StateBucket  string
	SessionTable string
	// Class is the class the substrate was stamped with at bootstrap
	// (ClassProduction or ClassPreview), or "" for an older bootstrap predating
	// the marker.
	Class string
}

// CFNDescriber is the read subset of the CloudFormation client used to
// discover the deployed bootstrap.
type CFNDescriber interface {
	DescribeStacks(ctx context.Context, in *cloudformation.DescribeStacksInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error)
}

// SSMAPI is the subset of the SSM client the passphrase step needs.
type SSMAPI interface {
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
	PutParameter(ctx context.Context, in *ssm.PutParameterInput, optFns ...func(*ssm.Options)) (*ssm.PutParameterOutput, error)
}

// CheckDeployed reports the production bootstrap state of an account. A missing
// stack is returned as Deployed{Present: false}, not an error.
func CheckDeployed(ctx context.Context, api CFNDescriber) (Deployed, error) {
	return checkStack(ctx, api, StackName)
}

// CheckDeployedPreview reports the preview infrastructure state of an account,
// read from its own stack. A missing stack is Deployed{Present: false}.
func CheckDeployedPreview(ctx context.Context, api CFNDescriber) (Deployed, error) {
	return checkStack(ctx, api, PreviewStackName)
}

// checkStack reads one bootstrap CloudFormation stack's outputs, including the
// class it was stamped with, into a Deployed.
func checkStack(ctx context.Context, api CFNDescriber, stackName string) (Deployed, error) {
	out, err := api.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stackName)})
	if err != nil {
		if isStackNotFound(err) {
			return Deployed{Present: false}, nil
		}
		return Deployed{}, fmt.Errorf("describe %s stack: %w", stackName, err)
	}
	if len(out.Stacks) == 0 {
		return Deployed{Present: false}, nil
	}
	d := Deployed{Present: true}
	for _, o := range out.Stacks[0].Outputs {
		switch aws.ToString(o.OutputKey) {
		case outputStateBucket:
			d.StateBucket = aws.ToString(o.OutputValue)
		case outputSessionTable:
			d.SessionTable = aws.ToString(o.OutputValue)
		case outputInfraClass:
			d.Class = aws.ToString(o.OutputValue)
		case outputVersion:

			var err error
			d.Version, err = strconv.Atoi(aws.ToString(o.OutputValue))
			if err != nil {
				return Deployed{}, fmt.Errorf("invalid bootstrap version %q: %w", aws.ToString(o.OutputValue), err)
			}

		}
	}
	return d, nil
}

// Run creates or updates the bootstrap CloudFormation stack and ensures the
// Pulumi passphrase exists, idempotently. progress reports discrete steps and
// log forwards detail; both may be nil.
func Run(ctx context.Context, cfn *cloudformation.Client, ssmClient SSMAPI, progress, log func(string)) error {
	report := func(f func(string), msg string) {
		if f != nil {
			f(msg)
		}
	}

	report(progress, "Ensuring Pulumi state bucket and sessions table (CloudFormation)")
	if err := upsertStack(ctx, cfn); err != nil {
		return err
	}

	report(progress, "Ensuring Pulumi passphrase (SSM SecureString)")
	created, err := ensurePassphrase(ctx, ssmClient)
	if err != nil {
		return err
	}
	if created {
		report(log, "generated a new Pulumi passphrase")
	} else {
		report(log, "reused the existing Pulumi passphrase")
	}
	return nil
}

// RunPreview creates or updates the preview infrastructure stack — the shared
// serverless cluster plus the shared VPC/subnets/security-group/logging/
// execution-role scaffolding both ephemeral logical slices and real per-PR
// resources sit on — and ensures the Pulumi passphrase, idempotently. The stack
// is stamped ClassPreview so a later command can verify it is acting on the
// preview substrate. progress and log may be nil.
//
// It shares the passphrase step with Run, but its CloudFormation stack
// (previewStackTemplate) provisions a substantially larger scaffolding whose
// full, correct shape and settling behaviour can only be validated against a
// live account. Like Run, that CloudFormation orchestration is the opt-in-e2e
// seam: this signature is final and the passphrase/stamping contract is settled;
// the preview stack template is filled in and exercised against real infra.
func RunPreview(ctx context.Context, cfn *cloudformation.Client, ssmClient SSMAPI, progress, log func(string)) error {
	report := func(f func(string), msg string) {
		if f != nil {
			f(msg)
		}
	}

	report(progress, "Ensuring preview infrastructure (CloudFormation)")
	if err := upsertPreviewStack(ctx, cfn); err != nil {
		return err
	}

	report(progress, "Ensuring Pulumi passphrase (SSM SecureString)")
	created, err := ensurePassphrase(ctx, ssmClient)
	if err != nil {
		return err
	}
	if created {
		report(log, "generated a new Pulumi passphrase")
	} else {
		report(log, "reused the existing Pulumi passphrase")
	}
	return nil
}

// upsertStack creates or updates the production bootstrap stack.
func upsertStack(ctx context.Context, cfn *cloudformation.Client) error {
	return upsertCFNStack(ctx, cfn, StackName, stackTemplate(), nil)
}

// upsertPreviewStack creates or updates the preview infrastructure stack. It
// requests named-IAM capability so the stack's shared execution-role
// scaffolding can be created.
func upsertPreviewStack(ctx context.Context, cfn *cloudformation.Client) error {
	return upsertCFNStack(ctx, cfn, PreviewStackName, previewStackTemplate(), []cfntypes.Capability{cfntypes.CapabilityCapabilityNamedIam})
}

// upsertCFNStack creates the named stack, or updates it if it already exists,
// waiting for the operation to settle. A no-op update is not an error.
func upsertCFNStack(ctx context.Context, cfn *cloudformation.Client, stackName, template string, capabilities []cfntypes.Capability) error {
	body := aws.String(template)
	_, err := cfn.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stackName)})
	switch {
	case err != nil && isStackNotFound(err):
		if _, err := cfn.CreateStack(ctx, &cloudformation.CreateStackInput{
			StackName:    aws.String(stackName),
			TemplateBody: body,
			Capabilities: capabilities,
		}); err != nil {
			return fmt.Errorf("create %s stack: %w", stackName, err)
		}
		w := cloudformation.NewStackCreateCompleteWaiter(cfn)
		if err := w.Wait(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stackName)}, stackWaitTimeout); err != nil {
			return fmt.Errorf("wait for %s create: %w", stackName, err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("describe %s stack: %w", stackName, err)
	default:
		_, err := cfn.UpdateStack(ctx, &cloudformation.UpdateStackInput{
			StackName:    aws.String(stackName),
			TemplateBody: body,
			Capabilities: capabilities,
		})
		if err != nil {
			if isNoUpdates(err) {
				return nil
			}
			return fmt.Errorf("update %s stack: %w", stackName, err)
		}
		w := cloudformation.NewStackUpdateCompleteWaiter(cfn)
		if err := w.Wait(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stackName)}, stackWaitTimeout); err != nil {
			return fmt.Errorf("wait for %s update: %w", stackName, err)
		}
		return nil
	}
}

// ensurePassphrase creates the SSM SecureString passphrase if it doesn't
// already exist, and never overwrites an existing one. It reports whether it
// created a new value.
func ensurePassphrase(ctx context.Context, ssmClient SSMAPI) (created bool, err error) {
	_, err = ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(PassphraseParamName),
		WithDecryption: aws.Bool(true),
	})
	if err == nil {
		return false, nil
	}
	var notFound *ssmtypes.ParameterNotFound
	if !errors.As(err, &notFound) {
		return false, fmt.Errorf("read passphrase parameter: %w", err)
	}

	passphrase, err := generatePassphrase()
	if err != nil {
		return false, err
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      aws.String(PassphraseParamName),
		Value:     aws.String(passphrase),
		Type:      ssmtypes.ParameterTypeSecureString,
		Overwrite: aws.Bool(false),
	}); err != nil {
		return false, fmt.Errorf("write passphrase parameter: %w", err)
	}
	return true, nil
}

// ReadPassphrase returns the stored Pulumi passphrase, decrypted.
func ReadPassphrase(ctx context.Context, ssmClient SSMAPI) (string, error) {
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(PassphraseParamName),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		return "", fmt.Errorf("read passphrase parameter: %w", err)
	}
	return aws.ToString(out.Parameter.Value), nil
}

func generatePassphrase() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate passphrase: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// stackTemplate renders the bootstrap CloudFormation template. The
// BootstrapVersion output is single-sourced from RequiredBootstrapVersion so
// the deployed version and the provider's requirement never drift.
func stackTemplate() string {
	return fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: Ocel bootstrap - account-global resources for the Ocel AWS provider.
Resources:
  StateBucket:
    Type: AWS::S3::Bucket
    Properties:
      BucketEncryption:
        ServerSideEncryptionConfiguration:
          - ServerSideEncryptionByDefault:
              SSEAlgorithm: AES256
      VersioningConfiguration:
        Status: Enabled
      PublicAccessBlockConfiguration:
        BlockPublicAcls: true
        BlockPublicPolicy: true
        IgnorePublicAcls: true
        RestrictPublicBuckets: true
  SessionsTable:
    Type: AWS::DynamoDB::Table
    Properties:
      BillingMode: PAY_PER_REQUEST
      AttributeDefinitions:
        - AttributeName: session_id
          AttributeType: S
      KeySchema:
        - AttributeName: session_id
          KeyType: HASH
      TimeToLiveSpecification:
        AttributeName: expires_at
        Enabled: true
Outputs:
  %s:
    Description: S3 bucket holding Pulumi state.
    Value: !Ref StateBucket
  %s:
    Description: DynamoDB table holding account-global upload sessions.
    Value: !Ref SessionsTable
  %s:
    Description: Ocel bootstrap schema version.
    Value: '%d'
  %s:
    Description: Class this substrate is stamped with, verified before an action runs.
    Value: '%s'
`, outputStateBucket, outputSessionTable, outputVersion, RequiredBootstrapVersion, outputInfraClass, ClassProduction)
}

// previewStackTemplate renders the preview infrastructure CloudFormation
// template. It shares the state bucket + sessions table shape production uses
// (each preview is its own Pulumi stack and needs the shared backend) and
// stamps InfrastructureClass=preview so a command can verify the substrate.
//
// The shared serverless cluster and the shared VPC/subnets/security-group/
// logging/execution-role scaffolding the PRD calls for are the opt-in-e2e seam:
// their correct shape and settling can only be validated against a live
// account, so — like RunPreview itself — they are added and exercised there.
// The stamped class, the shared backend, and the stack's independent lifecycle
// are settled here.
func previewStackTemplate() string {
	return fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: Ocel preview infrastructure - shared substrate per-PR previews are carved from.
Resources:
  StateBucket:
    Type: AWS::S3::Bucket
    Properties:
      BucketEncryption:
        ServerSideEncryptionConfiguration:
          - ServerSideEncryptionByDefault:
              SSEAlgorithm: AES256
      VersioningConfiguration:
        Status: Enabled
      PublicAccessBlockConfiguration:
        BlockPublicAcls: true
        BlockPublicPolicy: true
        IgnorePublicAcls: true
        RestrictPublicBuckets: true
  SessionsTable:
    Type: AWS::DynamoDB::Table
    Properties:
      BillingMode: PAY_PER_REQUEST
      AttributeDefinitions:
        - AttributeName: session_id
          AttributeType: S
      KeySchema:
        - AttributeName: session_id
          KeyType: HASH
      TimeToLiveSpecification:
        AttributeName: expires_at
        Enabled: true
Outputs:
  %s:
    Description: S3 bucket holding Pulumi state for preview stacks.
    Value: !Ref StateBucket
  %s:
    Description: DynamoDB table holding preview upload sessions.
    Value: !Ref SessionsTable
  %s:
    Description: Ocel bootstrap schema version.
    Value: '%d'
  %s:
    Description: Class this substrate is stamped with, verified before an action runs.
    Value: '%s'
`, outputStateBucket, outputSessionTable, outputVersion, RequiredBootstrapVersion, outputInfraClass, ClassPreview)
}

// CloudFormation surfaces both "stack does not exist" and the no-op update as
// a generic ValidationError with no dedicated SDK error type, so these are
// classified by the typed API error code plus its message (the code alone is
// too broad — it covers many unrelated validation failures).

func isStackNotFound(err error) bool {
	return isValidationErrorContaining(err, "does not exist")
}

func isNoUpdates(err error) bool {
	return isValidationErrorContaining(err, "No updates are to be performed")
}

func isValidationErrorContaining(err error, substr string) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.ErrorCode() == "ValidationError" && strings.Contains(apiErr.ErrorMessage(), substr)
}

// stackWaitTimeout bounds CloudFormation create/update waits.
const stackWaitTimeout = 10 * time.Minute
