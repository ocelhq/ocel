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

	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	"github.com/ocelhq/ocel/platform/aws/provider/stackindex"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	StackName = "ocel-bootstrap"

	PreviewStackName = "ocel-bootstrap-preview"

	PassphraseParamName = "/ocel/pulumi/passphrase"

	EdgeUserName        = "ocel-edge"
	EdgePreviewUserName = "ocel-edge-preview"

	StateTableIndexName = stackindex.IndexName

	outputStateBucket    = "StateBucketName"
	outputStateTable     = "StateTableName"
	outputArtifactBucket = "ArtifactBucketName"
	outputAssetBucket    = "AssetBucketName"
	outputVersion        = "BootstrapVersion"
	outputInfraClass     = "InfrastructureClass"

	artifactExpirationDays     = 30
	artifactAbortMultipartDays = 7

	stateNoncurrentDays     = 7
	stateAbortMultipartDays = 7
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

type stackPayloads struct {
	optimizer   payloads.Placement
	publisher   payloads.Placement
	invalidator payloads.Placement
	revalidator payloads.Placement
}

type substrate struct {
	class     string
	stackName string
	stackStep string
	template  func(edge.TrustBoundary, stackPayloads, int) string
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

func Run(ctx context.Context, cfn CFNAPI, ssmClient SSMAPI, iamClient IAMAPI, edgeProvider edge.Edge, store ObjectStore, progress, log func(string)) error {
	return run(ctx, cfn, ssmClient, iamClient, edgeProvider, store, productionSubstrate(), progress, log)
}

func RunPreview(ctx context.Context, cfn CFNAPI, ssmClient SSMAPI, iamClient IAMAPI, edgeProvider edge.Edge, store ObjectStore, progress, log func(string)) error {
	return run(ctx, cfn, ssmClient, iamClient, edgeProvider, store, previewSubstrate(), progress, log)
}

func run(ctx context.Context, cfn CFNAPI, ssmClient SSMAPI, iamClient IAMAPI, edgeProvider edge.Edge, store ObjectStore, sub substrate, progress, log func(string)) error {
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

	report(progress, "Ensuring the secret the origin's own front authenticates with (SSM SecureString)")
	if _, err := ensureOriginSecret(ctx, ssmClient, sub.class); err != nil {
		return err
	}

	report(progress, sub.stackStep)
	namedIAM := []cfntypes.Capability{cfntypes.CapabilityCapabilityNamedIam}

	deployed, err := checkStack(ctx, cfn, sub.stackName)
	if err != nil {
		return err
	}
	if deployed.ArtifactBucket == "" {
		if err := upsertCFNStack(ctx, cfn, sub.stackName, sub.template(edgeOut.Trust, stackPayloads{}, seedingBootstrapVersion), namedIAM); err != nil {
			return err
		}
		if deployed, err = checkStack(ctx, cfn, sub.stackName); err != nil {
			return err
		}
	}

	var code stackPayloads
	if code.optimizer, err = ensureOptimizerPayload(ctx, store, deployed.ArtifactBucket); err != nil {
		return err
	}
	if isrWriterAdopted {
		if code.publisher, err = ensureTagPublisherPayload(ctx, store, deployed.ArtifactBucket); err != nil {
			return err
		}
	}
	if invalidatesOnPromote(edgeProvider) {
		if code.invalidator, err = ensureTagInvalidatorPayload(ctx, store, deployed.ArtifactBucket); err != nil {
			return err
		}
	}
	if code.revalidator, err = ensureRevalidatorPayload(ctx, store, deployed.ArtifactBucket); err != nil {
		return err
	}
	if !isrWriterAdopted {
		report(log, "this substrate adopted no ISR writer, so no tag publisher is created; there is no edge replica for it to publish into")
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
		Name:        aws.String(PassphraseParamName),
		Description: aws.String("Ocel: the passphrase every Pulumi stack in this account is encrypted under, production and preview alike. Generated once by ocel bootstrap and never rotated. It is the only copy - delete it and the state in the Pulumi state buckets can never be decrypted again, stranding every app Ocel has deployed here."),
		Value:       aws.String(passphrase),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(false),
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

func stackTemplate(trust edge.TrustBoundary, code stackPayloads, version int) string {
	return fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: "Ocel bootstrap (production) - the account-global substrate every production app Ocel deploys into this AWS account is built on: the Pulumi state bucket and state table, the artifact and asset buckets, the variable store, and the image optimizer, tag publisher, tag invalidator and ISR revalidator all apps share. Created and updated by ocel bootstrap; it holds no app of its own. Deleting this stack orphans every app deployed from it: the Pulumi state describing them goes with its bucket, and no deploy or teardown can run until it is recreated."
Resources:
%s%s%s%s%s%s%s%s%s%s%s%sOutputs:
  %s:
    Description: "S3 bucket holding the Pulumi state Ocel plans every production deploy and teardown from. One versioned object per app stack."
    Value: !Ref StateBucket
%s%s%s%s%s%s  %s:
    Description: "Schema version of this bootstrap stack. The CLI refuses to act while its required version and this one disagree, and points at the side that has to move."
    Value: '%d'
  %s:
    Description: "Class this substrate is stamped with, verified before an action runs so that a preview deploy cannot reach production state, variables or caches."
    Value: '%s'
`, stateBucketResource(ClassProduction), stateTableResource(), artifactBucketResource(), assetBucketResource(), assetBucketPolicyResource(), varsResources(ClassProduction), imageOptimizerResources(code.optimizer), tagPublisherResources(code.publisher, ClassProduction), tagInvalidatorResources(code.invalidator, ClassProduction), revalidateQueueResources(ClassProduction), revalidatorResources(code.revalidator), edgeUserResource(EdgeUserName, ClassProduction, trust, code.optimizer), outputStateBucket, stateTableOutput(), artifactBucketOutput(), assetBucketOutput(), varsOutputs(), imageOptimizerOutput(code.optimizer), revalidateQueueOutput(code.revalidator), outputVersion, version, outputInfraClass, ClassProduction)
}

func previewStackTemplate(trust edge.TrustBoundary, code stackPayloads, version int) string {
	return fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: "Ocel bootstrap (preview) - the account-global substrate every preview environment Ocel deploys into this AWS account is carved from, deliberately separate from the production bootstrap so a per-PR preview can never reach production state, variables or caches. Created and updated by ocel bootstrap --preview. Deleting this stack orphans every live preview: the Pulumi state describing them goes with its bucket, and no preview deploy or teardown can run until it is recreated."
Resources:
%s%s%s%s%s%s%s%s%s%s%s%sOutputs:
  %s:
    Description: "S3 bucket holding the Pulumi state Ocel plans every preview deploy and teardown from. One versioned object per preview stack."
    Value: !Ref StateBucket
%s%s%s%s%s%s  %s:
    Description: "Schema version of this bootstrap stack. The CLI refuses to act while its required version and this one disagree, and points at the side that has to move."
    Value: '%d'
  %s:
    Description: "Class this substrate is stamped with, verified before an action runs so that a preview deploy cannot reach production state, variables or caches."
    Value: '%s'
`, stateBucketResource(ClassPreview), stateTableResource(), artifactBucketResource(), assetBucketResource(), assetBucketPolicyResource(), varsResources(ClassPreview), imageOptimizerResources(code.optimizer), tagPublisherResources(code.publisher, ClassPreview), tagInvalidatorResources(code.invalidator, ClassPreview), revalidateQueueResources(ClassPreview), revalidatorResources(code.revalidator), edgeUserResource(EdgePreviewUserName, ClassPreview, trust, code.optimizer), outputStateBucket, stateTableOutput(), artifactBucketOutput(), assetBucketOutput(), varsOutputs(), imageOptimizerOutput(code.optimizer), revalidateQueueOutput(code.revalidator), outputVersion, version, outputInfraClass, ClassPreview)
}

func stateBucketResource(class string) string {
	scope := "production app"
	if class == ClassPreview {
		scope = "preview environment"
	}
	return fmt.Sprintf(`  StateBucket:
    Type: AWS::S3::Bucket
    Metadata:
      Description: "Pulumi state for every %s Ocel has deployed into this account, one versioned object per stack. Ocel reads it to plan a deploy and to tear one down, so emptying or deleting this bucket strands what is already deployed: the infrastructure keeps running and Ocel can no longer describe, update or remove it."
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
      LifecycleConfiguration:
        Rules:
          - Id: expire-noncurrent-state
            Status: Enabled
            NoncurrentVersionExpiration:
              NoncurrentDays: %d
            AbortIncompleteMultipartUpload:
              DaysAfterInitiation: %d
          - Id: expire-state-delete-markers
            Status: Enabled
            ExpiredObjectDeleteMarker: true
`, scope, stateNoncurrentDays, stateAbortMultipartDays)
}

func stateTableResource() string {
	return fmt.Sprintf(`  StateTable:
    Type: AWS::DynamoDB::Table
    Metadata:
      Description: "Account-global Ocel state, keyed by pk/sk: the index of every stack this substrate has deployed, which prune and teardown walk, and the tag clock the edge reads and updates to decide whether a cached page is still fresh. Deleting it loses both - Ocel forgets what it deployed here, and every tag's invalidation history starts over."
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
    Description: "DynamoDB table holding account-global Ocel state keyed by pk/sk: the stack index prune and teardown walk, and the ISR tag clock every app in this substrate shares with the edge."
    Value: !Ref StateTable
`, outputStateTable)
}

func artifactBucketResource() string {
	return fmt.Sprintf(`  ArtifactBucket:
    Type: AWS::S3::Bucket
    Metadata:
      Description: "Staging area for the Lambda code Ocel uploads before a stack references it. A deployed function holds its own copy and these objects age out on a lifecycle rule, so deleting this bucket costs the next bootstrap and the next deploy a re-upload, not a running app."
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
    Description: "S3 bucket Ocel stages function code in before a stack references it. Objects age out on a lifecycle rule; a deployed function keeps its own copy."
    Value: !Ref ArtifactBucket
`, outputArtifactBucket)
}

func assetBucketResource() string {
	return fmt.Sprintf(`  AssetBucket:
    Type: AWS::S3::Bucket
    Metadata:
      Description: "Per-build static assets, prerender fallbacks, image-optimizer config and the edge's fetch cache, keyed by build id. The edge, the image optimizer, the tag publisher and the revalidator all read it directly, so deleting it breaks static assets and image optimization for every app in this substrate until each is redeployed."
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

func assetBucketPolicyResource() string {
	return `  AssetBucketPolicy:
    Type: AWS::S3::BucketPolicy
    Metadata:
      Description: "The only grant on the asset bucket. It lets CloudFront distributions in this account, and no one else, read objects out of it over an origin access control; the bucket itself stays closed to the public. One statement for the whole account, so no deploy ever rewrites it and concurrent deploys cannot clobber each other."
    Properties:
      Bucket: !Ref AssetBucket
      PolicyDocument:
        Version: '2012-10-17'
        Statement:
          - Sid: AllowCloudFrontOriginAccessControlRead
            Effect: Allow
            Principal:
              Service: cloudfront.amazonaws.com
            Action: s3:GetObject
            Resource: !Sub '${AssetBucket.Arn}/*'
            Condition:
              StringEquals:
                AWS:SourceAccount: !Ref AWS::AccountId
`
}

func assetBucketOutput() string {
	return fmt.Sprintf(`  %s:
    Description: "S3 bucket holding per-build static assets, prerender fallbacks, image-optimizer config and the edge fetch cache, keyed by build id. Read directly by the edge."
    Value: !Ref AssetBucket
`, outputAssetBucket)
}

func edgeUserResource(userName, class string, trust edge.TrustBoundary, optimizer payloads.Placement) string {
	if trust != edge.TrustExternal {
		return ""
	}
	return fmt.Sprintf(`  EdgeUser:
    Type: AWS::IAM::User
    Metadata:
      Description: "The identity the %s edge signs its calls into this account with, from outside the trust boundary: it reads the asset bucket, writes the fetch cache back, reads and updates tag items, invokes the app functions Ocel deploys and enqueues ISR revalidations. Nothing else in this account assumes it. Its access key is the only credential the edge holds; delete the user or its key and the edge is severed from this account, and bootstrap only mints a replacement once the SSM parameter holding the old one is gone too."
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
                      - 'PROJECT#*#TAG#*'
              - Effect: Allow
                Action: dynamodb:Query
                Resource: !Sub '${StateTable.Arn}/index/%s'
                Condition:
                  ForAllValues:StringLike:
                    dynamodb:LeadingKeys:
                      - 'PROJECT#*#TAG#*'
              - Effect: Allow
                Action:
                  - lambda:InvokeFunctionUrl
                  - lambda:InvokeFunction
                Resource: !Sub 'arn:aws:lambda:*:${AWS::AccountId}:function:*'
                Condition:
                  StringEquals:
                    'aws:ResourceTag/ocel:component': 'function'
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
%s`, class, userName, StateTableIndexName, imageOptimizerInvokeStatement(optimizer))
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
