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

	"github.com/ocelhq/ocel/cloud/edge"
)

const (
	StackName = "ocel-bootstrap"

	PreviewStackName = "ocel-bootstrap-preview"

	PassphraseParamName = "/ocel/pulumi/passphrase"

	EdgeUserName        = "ocel-edge"
	EdgePreviewUserName = "ocel-edge-preview"

	StateTableIndexName = "gsi1"

	outputStateBucket    = "StateBucketName"
	outputStateTable     = "StateTableName"
	outputArtifactBucket = "ArtifactBucketName"
	outputAssetBucket    = "AssetBucketName"
	outputVersion        = "BootstrapVersion"
	outputInfraClass     = "InfrastructureClass"

	artifactExpirationDays     = 30
	artifactAbortMultipartDays = 7
)

const (
	ClassProduction = "production"
	ClassPreview    = "preview"
)

type Deployed struct {
	Present            bool
	Version            int
	StateBucket        string
	StateTable         string
	ArtifactBucket     string
	AssetBucket        string
	VarsTable          string
	VarsKeyARN         string
	ImageOptimizerURL  string
	RevalidateQueueURL string
	Class              string
}

type CFNDescriber interface {
	DescribeStacks(ctx context.Context, in *cloudformation.DescribeStacksInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error)
}

type CFNAPI interface {
	CFNDescriber
	CreateStack(ctx context.Context, in *cloudformation.CreateStackInput, optFns ...func(*cloudformation.Options)) (*cloudformation.CreateStackOutput, error)
	UpdateStack(ctx context.Context, in *cloudformation.UpdateStackInput, optFns ...func(*cloudformation.Options)) (*cloudformation.UpdateStackOutput, error)
}

type SSMAPI interface {
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
	PutParameter(ctx context.Context, in *ssm.PutParameterInput, optFns ...func(*ssm.Options)) (*ssm.PutParameterOutput, error)
	DeleteParameter(ctx context.Context, in *ssm.DeleteParameterInput, optFns ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error)
}

func CheckDeployed(ctx context.Context, api CFNDescriber) (Deployed, error) {
	return checkStack(ctx, api, StackName)
}

func CheckDeployedPreview(ctx context.Context, api CFNDescriber) (Deployed, error) {
	return checkStack(ctx, api, PreviewStackName)
}

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
		case outputStateTable:
			d.StateTable = aws.ToString(o.OutputValue)
		case outputArtifactBucket:
			d.ArtifactBucket = aws.ToString(o.OutputValue)
		case outputAssetBucket:
			d.AssetBucket = aws.ToString(o.OutputValue)
		case outputVarsTable:
			d.VarsTable = aws.ToString(o.OutputValue)
		case outputVarsKeyARN:
			d.VarsKeyARN = aws.ToString(o.OutputValue)
		case outputImageOptimizerURL:
			d.ImageOptimizerURL = aws.ToString(o.OutputValue)
		case outputRevalidateQueueURL:
			d.RevalidateQueueURL = aws.ToString(o.OutputValue)
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

type stackArtifacts struct {
	optimizer   artifactCode
	publisher   artifactCode
	revalidator artifactCode
}

func (a stackArtifacts) present() bool {
	return a.optimizer.present() || a.publisher.present() || a.revalidator.present()
}

type stackPins struct {
	optimizer   artifactPin
	publisher   artifactPin
	revalidator artifactPin
}

func (p stackPins) pinned() bool {
	return p.optimizer.pinned() || p.publisher.pinned() || p.revalidator.pinned()
}

func pinnedArtifacts() stackPins {
	return stackPins{optimizer: pinnedOptimizer(), publisher: pinnedTagPublisher(), revalidator: pinnedRevalidator()}
}

type substrate struct {
	class     string
	stackName string
	stackStep string
	template  func(edge.TrustBoundary, stackArtifacts, int) string
}

func productionSubstrate() substrate {
	return substrate{
		class:     ClassProduction,
		stackName: StackName,
		stackStep: "Ensuring Pulumi state bucket and state table (CloudFormation)",
		template:  stackTemplate,
	}
}

func previewSubstrate() substrate {
	return substrate{
		class:     ClassPreview,
		stackName: PreviewStackName,
		stackStep: "Ensuring preview infrastructure (CloudFormation)",
		template:  previewStackTemplate,
	}
}

func Run(ctx context.Context, cfn CFNAPI, ssmClient SSMAPI, iamClient IAMAPI, edgeProvider edge.Provider, artifact Artifacts, progress, log func(string)) error {
	return run(ctx, cfn, ssmClient, iamClient, edgeProvider, artifact, pinnedArtifacts(), productionSubstrate(), progress, log)
}

func RunPreview(ctx context.Context, cfn CFNAPI, ssmClient SSMAPI, iamClient IAMAPI, edgeProvider edge.Provider, artifact Artifacts, progress, log func(string)) error {
	return run(ctx, cfn, ssmClient, iamClient, edgeProvider, artifact, pinnedArtifacts(), previewSubstrate(), progress, log)
}

func run(ctx context.Context, cfn CFNAPI, ssmClient SSMAPI, iamClient IAMAPI, edgeProvider edge.Provider, artifact Artifacts, pins stackPins, sub substrate, progress, log func(string)) error {
	report := func(f func(string), msg string) {
		if f != nil {
			f(msg)
		}
	}

	report(progress, fmt.Sprintf("Bootstrapping the %s edge", edgeProvider.Kind()))
	edgeOut, err := edgeProvider.Bootstrap(ctx, edge.Class(sub.class))
	if err != nil {
		return fmt.Errorf("bootstrap %s edge: %w", edgeProvider.Kind(), err)
	}
	var isrWriterAdopted bool
	for _, offer := range edgeOut.Offers {
		switch offer.Kind {
		case edge.OfferCacheStore:
			report(progress, "Adopting the edge cache store (SSM SecureString)")
			if err := adoptCacheStore(ctx, ssmClient, sub.class, edgeProvider.Kind(), offer.Values); err != nil {
				return err
			}
		case edge.OfferDeploymentsStore:
			report(progress, "Adopting the deployments-store worker (SSM SecureString)")
			if err := adoptDeploymentsStore(ctx, ssmClient, sub.class, offer.Values); err != nil {
				return err
			}
		case edge.OfferISRWriter:
			report(progress, "Adopting the ISR writer worker (SSM SecureString)")
			if err := adoptISRWriter(ctx, ssmClient, sub.class, offer.Values); err != nil {
				return err
			}
			if _, err := ensureISRWriterSeed(ctx, ssmClient, sub.class); err != nil {
				return err
			}
			isrWriterAdopted = true
		default:
			report(log, fmt.Sprintf("ignoring edge offer %q: no provider resource adopts it", offer.Kind))
		}
	}

	report(progress, sub.stackStep)
	namedIAM := []cfntypes.Capability{cfntypes.CapabilityCapabilityNamedIam}

	deployed, err := checkStack(ctx, cfn, sub.stackName)
	if err != nil {
		return err
	}
	if pins.pinned() && deployed.ArtifactBucket == "" {
		if err := upsertCFNStack(ctx, cfn, sub.stackName, sub.template(edgeOut.Trust, stackArtifacts{}, seedingBootstrapVersion), namedIAM); err != nil {
			return err
		}
		if deployed, err = checkStack(ctx, cfn, sub.stackName); err != nil {
			return err
		}
	}

	var code stackArtifacts
	if code.optimizer, err = ensureOptimizerArtifact(ctx, artifact, deployed.ArtifactBucket, pins.optimizer); err != nil {
		return err
	}
	if isrWriterAdopted {
		if code.publisher, err = ensureTagPublisherArtifact(ctx, artifact, deployed.ArtifactBucket, pins.publisher); err != nil {
			return err
		}
	}
	if code.revalidator, err = ensureRevalidatorArtifact(ctx, artifact, deployed.ArtifactBucket, pins.revalidator); err != nil {
		return err
	}
	if !code.optimizer.present() {
		report(log, "no image optimizer artifact is pinned in this provider build; none is created, and /_next/image answers 502 as it did before")
	}
	if !code.revalidator.present() {
		report(log, "no revalidator artifact is pinned in this provider build; the revalidation queue is created but nothing drains it, so the edge is not told its URL and every admitted refresh renders through the origin as it does today")
	}
	if !isrWriterAdopted {
		report(log, "this substrate adopted no ISR writer, so no tag publisher is created; there is no edge replica for it to publish into")
	} else if !code.publisher.present() {
		report(log, "no tag publisher artifact is pinned in this provider build; none is created, so no origin-raised invalidation reaches a build's edge replica — only the ones raised at the edge itself do. The origin's own tag state is authoritative and unaffected")
	}

	if err := upsertCFNStack(ctx, cfn, sub.stackName, sub.template(edgeOut.Trust, code, RequiredBootstrapVersion), namedIAM); err != nil {
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

	if len(edgeOut.Values) > 0 {
		report(progress, "Storing edge bootstrap outputs (SSM SecureString)")
		if err := writeEdgeValues(ctx, ssmClient, sub.class, edgeOut.Values); err != nil {
			return err
		}
	}

	if edgeOut.Trust != edge.TrustExternal {
		report(log, "edge runs inside the trust boundary; no edge reader or static key created")
		return nil
	}

	report(progress, "Ensuring edge reader credentials (SSM SecureString)")
	created, err = ensureEdgeCredentials(ctx, iamClient, ssmClient, sub.class)
	if err != nil {
		return err
	}
	if created {
		report(log, "minted a new edge reader access key")
	} else {
		report(log, "reused the existing edge reader access key")
	}
	return nil
}

func upsertCFNStack(ctx context.Context, cfn CFNAPI, stackName, template string, capabilities []cfntypes.Capability) error {
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

func stackTemplate(trust edge.TrustBoundary, code stackArtifacts, version int) string {
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
%s%s%s%s%s%s%s%s%sOutputs:
  %s:
    Description: S3 bucket holding Pulumi state.
    Value: !Ref StateBucket
%s%s%s%s%s%s  %s:
    Description: Ocel bootstrap schema version.
    Value: '%d'
  %s:
    Description: Class this substrate is stamped with, verified before an action runs.
    Value: '%s'
`, stateTableResource(), artifactBucketResource(), assetBucketResource(), varsResources(ClassProduction), imageOptimizerResources(code.optimizer), tagPublisherResources(code.publisher, ClassProduction), revalidateQueueResources(ClassProduction), revalidatorResources(code.revalidator), edgeUserResource(EdgeUserName, trust, code.optimizer), outputStateBucket, stateTableOutput(), artifactBucketOutput(), assetBucketOutput(), varsOutputs(), imageOptimizerOutput(code.optimizer), revalidateQueueOutput(code.revalidator), outputVersion, version, outputInfraClass, ClassProduction)
}

func previewStackTemplate(trust edge.TrustBoundary, code stackArtifacts, version int) string {
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
%s%s%s%s%s%s%s%s%sOutputs:
  %s:
    Description: S3 bucket holding Pulumi state for preview stacks.
    Value: !Ref StateBucket
%s%s%s%s%s%s  %s:
    Description: Ocel bootstrap schema version.
    Value: '%d'
  %s:
    Description: Class this substrate is stamped with, verified before an action runs.
    Value: '%s'
`, stateTableResource(), artifactBucketResource(), assetBucketResource(), varsResources(ClassPreview), imageOptimizerResources(code.optimizer), tagPublisherResources(code.publisher, ClassPreview), revalidateQueueResources(ClassPreview), revalidatorResources(code.revalidator), edgeUserResource(EdgePreviewUserName, trust, code.optimizer), outputStateBucket, stateTableOutput(), artifactBucketOutput(), assetBucketOutput(), varsOutputs(), imageOptimizerOutput(code.optimizer), revalidateQueueOutput(code.revalidator), outputVersion, version, outputInfraClass, ClassPreview)
}

func stateTableResource() string {
	return fmt.Sprintf(`  StateTable:
    Type: AWS::DynamoDB::Table
    Properties:
      BillingMode: PAY_PER_REQUEST
      AttributeDefinitions:
        - AttributeName: pk
          AttributeType: S
        - AttributeName: sk
          AttributeType: S
        - AttributeName: gsi1pk
          AttributeType: S
        - AttributeName: gsi1sk
          AttributeType: S
      KeySchema:
        - AttributeName: pk
          KeyType: HASH
        - AttributeName: sk
          KeyType: RANGE
      GlobalSecondaryIndexes:
        - IndexName: %s
          KeySchema:
            - AttributeName: gsi1pk
              KeyType: HASH
            - AttributeName: gsi1sk
              KeyType: RANGE
          Projection:
            ProjectionType: INCLUDE
            NonKeyAttributes:
              - expired
              - stale
              - tag
      TimeToLiveSpecification:
        AttributeName: expires_at
        Enabled: true
      StreamSpecification:
        StreamViewType: NEW_IMAGE
`, StateTableIndexName)
}

func stateTableOutput() string {
	return fmt.Sprintf(`  %s:
    Description: DynamoDB table holding account-global Ocel state, keyed by pk/sk.
    Value: !Ref StateTable
`, outputStateTable)
}

func artifactBucketResource() string {
	return fmt.Sprintf(`  ArtifactBucket:
    Type: AWS::S3::Bucket
    Properties:
      BucketEncryption:
        ServerSideEncryptionConfiguration:
          - ServerSideEncryptionByDefault:
              SSEAlgorithm: AES256
      PublicAccessBlockConfiguration:
        BlockPublicAcls: true
        BlockPublicPolicy: true
        IgnorePublicAcls: true
        RestrictPublicBuckets: true
      LifecycleConfiguration:
        Rules:
          - Id: expire-artifacts
            Status: Enabled
            ExpirationInDays: %d
            AbortIncompleteMultipartUpload:
              DaysAfterInitiation: %d
`, artifactExpirationDays, artifactAbortMultipartDays)
}

func artifactBucketOutput() string {
	return fmt.Sprintf(`  %s:
    Description: S3 bucket holding function deployment artifacts.
    Value: !Ref ArtifactBucket
`, outputArtifactBucket)
}

func assetBucketResource() string {
	return fmt.Sprintf(`  AssetBucket:
    Type: AWS::S3::Bucket
    Properties:
      BucketEncryption:
        ServerSideEncryptionConfiguration:
          - ServerSideEncryptionByDefault:
              SSEAlgorithm: AES256
      PublicAccessBlockConfiguration:
        BlockPublicAcls: true
        BlockPublicPolicy: true
        IgnorePublicAcls: true
        RestrictPublicBuckets: true
      LifecycleConfiguration:
        Rules:
          - Id: abort-incomplete-uploads
            Status: Enabled
            AbortIncompleteMultipartUpload:
              DaysAfterInitiation: %d
`, artifactAbortMultipartDays)
}

func assetBucketOutput() string {
	return fmt.Sprintf(`  %s:
    Description: S3 bucket holding prerender configs and fallbacks, keyed by build id.
    Value: !Ref AssetBucket
`, outputAssetBucket)
}

func edgeUserResource(userName string, trust edge.TrustBoundary, optimizer artifactCode) string {
	if trust != edge.TrustExternal {
		return ""
	}
	return fmt.Sprintf(`  EdgeUser:
    Type: AWS::IAM::User
    Properties:
      UserName: %s
      Policies:
        - PolicyName: ocel-edge-cache
          PolicyDocument:
            Version: '2012-10-17'
            Statement:
              - Effect: Allow
                Action: s3:GetObject
                Resource: !Sub '${AssetBucket.Arn}/*'
              - Effect: Allow
                Action: s3:PutObject
                Resource: !Sub '${AssetBucket.Arn}/*/fetch-cache/*.cache.json'
              - Effect: Allow
                Action:
                  - dynamodb:BatchGetItem
                  - dynamodb:UpdateItem
                Resource: !GetAtt StateTable.Arn
                Condition:
                  ForAllValues:StringLike:
                    dynamodb:LeadingKeys:
                      - 'TAG#*'
              - Effect: Allow
                Action: dynamodb:Query
                Resource: !Sub '${StateTable.Arn}/index/%s'
                Condition:
                  ForAllValues:StringLike:
                    dynamodb:LeadingKeys:
                      - 'TAG#*'
              - Effect: Allow
                Action:
                  - lambda:InvokeFunctionUrl
                  - lambda:InvokeFunction
                Resource: !Sub 'arn:aws:lambda:*:${AWS::AccountId}:function:*'
                Condition:
                  'Null':
                    'aws:ResourceTag/ocel:app': 'false'
              - Effect: Allow
                Action: sqs:SendMessage
                Resource: !GetAtt RevalidateQueue.Arn
              - Effect: Allow
                Action:
                  - kms:GenerateDataKey
                  - kms:Decrypt
                Resource: '*'
                Condition:
                  StringEquals:
                    kms:ViaService: !Sub 'sqs.${AWS::Region}.amazonaws.com'
%s`, userName, StateTableIndexName, imageOptimizerInvokeStatement(optimizer))
}

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

const stackWaitTimeout = 10 * time.Minute
