// Package bootstrap provisions and inspects the account-global resources the
// Ocel AWS provider needs before any deploy can run: an S3 bucket for Pulumi
// state and a DynamoDB table for Ocel state (both via CloudFormation),
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

	"github.com/ocelhq/ocel/cloud/edge"
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

	// EdgeUserName / EdgePreviewUserName are the deterministic IAM user names the
	// per-substrate edge reader is provisioned under. The name is stable so the
	// imperative access-key step (ensureEdgeCredentials) can find the user without
	// a stack output, and so a redeploy updates it in place.
	EdgeUserName        = "ocel-edge"
	EdgePreviewUserName = "ocel-edge-preview"

	// StateTableIndexName is the state table's secondary index. Exported so the
	// deploy path can grant against it and name it to a function's runtime
	// rather than hardcoding a name this template alone controls. Named
	// generically, like the pk/sk pair, so a second entity can adopt it.
	StateTableIndexName = "gsi1"

	outputStateBucket    = "StateBucketName"
	outputStateTable     = "StateTableName"
	outputArtifactBucket = "ArtifactBucketName"
	outputAssetBucket    = "AssetBucketName"
	outputVersion        = "BootstrapVersion"
	outputInfraClass     = "InfrastructureClass"

	// artifactExpirationDays is how long a function deployment artifact lives in
	// the artifact bucket before the lifecycle rule expires it. It is comfortably
	// longer than any realistic deploy cadence: the deploy path re-uploads a live
	// function's artifact (skip-if-exists) on every deploy, so an artifact still
	// backing a function is always refreshed well before it can age out, and only
	// genuinely stale versions are reaped.
	artifactExpirationDays = 30
	// artifactAbortMultipartDays bounds how long an aborted/incomplete multipart
	// upload lingers before S3 reclaims its parts.
	artifactAbortMultipartDays = 7
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
	Present     bool
	Version     int
	StateBucket string
	// StateTable is the account-global DynamoDB table every Ocel state entity
	// keys into under a generic pk/sk pair: upload sessions, Next ISR tag
	// revalidation records, and whatever comes next.
	StateTable string
	// ArtifactBucket is the account-global S3 bucket function deployment
	// artifacts are uploaded to; the deploy path points Lambda code at it.
	ArtifactBucket string
	// AssetBucket is the account-global S3 bucket prerender configs + fallbacks
	// are uploaded to, keyed by build id; the deploy path crawls a Next app's
	// output for them and the runtime reads them to serve ISR.
	AssetBucket string
	// VarsTable is the account-global DynamoDB table variable values live in,
	// separate from the state table.
	VarsTable string
	// VarsKeyARN is the KMS key every encrypted value of this substrate's class
	// is encrypted under.
	VarsKeyARN string
	// ImageOptimizerURL is the Function URL of this substrate's image optimizer,
	// bound into every worker so /_next/image has somewhere to go. Empty on a
	// substrate whose bootstrap rendered no optimizer — an older bootstrap, or a
	// provider build pinning no artifact — which leaves image requests at the 502
	// they answered before the optimizer existed.
	ImageOptimizerURL string
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

// CFNAPI is the subset of the CloudFormation client the stack upsert needs. It
// embeds CFNDescriber so the create/update waiters, which only describe, accept
// it directly.
type CFNAPI interface {
	CFNDescriber
	CreateStack(ctx context.Context, in *cloudformation.CreateStackInput, optFns ...func(*cloudformation.Options)) (*cloudformation.CreateStackOutput, error)
	UpdateStack(ctx context.Context, in *cloudformation.UpdateStackInput, optFns ...func(*cloudformation.Options)) (*cloudformation.UpdateStackOutput, error)
}

// SSMAPI is the subset of the SSM client the passphrase step needs.
type SSMAPI interface {
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
	PutParameter(ctx context.Context, in *ssm.PutParameterInput, optFns ...func(*ssm.Options)) (*ssm.PutParameterOutput, error)
	DeleteParameter(ctx context.Context, in *ssm.DeleteParameterInput, optFns ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error)
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

// stackArtifacts is where the account-global Lambdas a stack renders read their
// code from. Each is independently absent: a build that pins one artifact and
// not the other renders one function and not the other, and each missing one
// degrades on its own terms.
type stackArtifacts struct {
	optimizer artifactCode
	publisher artifactCode
}

func (a stackArtifacts) present() bool { return a.optimizer.present() || a.publisher.present() }

// stackPins is what this provider build ships for those Lambdas. One pinned and
// the other not is a normal state: each is cut and pinned on its own release.
type stackPins struct {
	optimizer artifactPin
	publisher artifactPin
}

func (p stackPins) pinned() bool { return p.optimizer.pinned() || p.publisher.pinned() }

func pinnedArtifacts() stackPins {
	return stackPins{optimizer: pinnedOptimizer(), publisher: pinnedTagPublisher()}
}

// substrate is the per-class difference between the two bootstrap paths: which
// stack holds it, which template renders it, and what to call that step. The
// steps themselves are identical, so both classes share run.
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

// Run creates or updates the bootstrap CloudFormation stack and ensures the
// Pulumi passphrase exists, idempotently. progress reports discrete steps and
// log forwards detail; both may be nil.
func Run(ctx context.Context, cfn CFNAPI, ssmClient SSMAPI, iamClient IAMAPI, edgeProvider edge.Provider, artifact Artifacts, progress, log func(string)) error {
	return run(ctx, cfn, ssmClient, iamClient, edgeProvider, artifact, pinnedArtifacts(), productionSubstrate(), progress, log)
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
func RunPreview(ctx context.Context, cfn CFNAPI, ssmClient SSMAPI, iamClient IAMAPI, edgeProvider edge.Provider, artifact Artifacts, progress, log func(string)) error {
	return run(ctx, cfn, ssmClient, iamClient, edgeProvider, artifact, pinnedArtifacts(), previewSubstrate(), progress, log)
}

// run bootstraps one substrate, edge first. The edge is a pure producer here —
// nothing calls back into it — and what it reports decides what the provider
// then provisions: an edge outside the provider's trust boundary needs the edge
// reader IAM user and a static access key to sign its reads with, an edge inside
// it needs neither, so neither is created.
// pins are the artifacts this build ships; they are a parameter rather than read
// from the constants here so a test can exercise the whole placement path
// against fixture artifacts without a cut release.
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
	// An unrecognised kind is ignored rather than rejected, so a newer edge paired
	// with an older provider degrades instead of breaking — and dropping an
	// adoption here is enough to put the resource it replaced back in service.
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
			// The seed every build's write secret is derived from. It belongs to
			// the substrate rather than to a deploy run, because the secrets a
			// live build's functions already hold were derived from it — see
			// ensureISRWriterSeed.
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

	// The image optimizer's code must already sit in this account's own artifact
	// bucket before CloudFormation can point a function at it — and on a first
	// bootstrap that bucket is created by this very stack. So a first bootstrap
	// settles the stack once without the optimizer to raise the buckets, places
	// the artifact, and settles again; every later one already knows the bucket
	// and takes a single pass.
	//
	// On an account that already has the bucket, the artifact step runs before
	// anything is upserted, so a refused artifact leaves that account exactly as
	// it was. A first bootstrap cannot have that: its seeding pass is what creates
	// the bucket, and it necessarily precedes the artifact. So the seeding pass
	// stamps seedingBootstrapVersion — see version.go — and a refused artifact
	// leaves a stack that exists but does not satisfy the gate, which sends the
	// operator back to `ocel bootstrap` instead of letting an optimizer-less
	// account deploy and 502 every image.
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
	// The publisher raises through the writer's endpoint under a secret derived
	// from the substrate's seed, and a substrate that adopted no writer has
	// neither. Placed there anyway it would refuse to start on every invocation,
	// retire every batch to the dead-letter queue, and hold the alarm lit from
	// the moment of bootstrap.
	if isrWriterAdopted {
		if code.publisher, err = ensureTagPublisherArtifact(ctx, artifact, deployed.ArtifactBucket, pins.publisher); err != nil {
			return err
		}
	}
	if !code.optimizer.present() {
		report(log, "no image optimizer artifact is pinned in this provider build; none is created, and /_next/image answers 502 as it did before")
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

// upsertCFNStack creates the named stack, or updates it if it already exists,
// waiting for the operation to settle. A no-op update is not an error.
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

// stackTemplate renders the bootstrap CloudFormation template for an edge with
// the given trust posture. The BootstrapVersion output is single-sourced from
// RequiredBootstrapVersion so the deployed version and the provider's
// requirement never drift.
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
%s%s%s%s%s%s%sOutputs:
  %s:
    Description: S3 bucket holding Pulumi state.
    Value: !Ref StateBucket
%s%s%s%s%s  %s:
    Description: Ocel bootstrap schema version.
    Value: '%d'
  %s:
    Description: Class this substrate is stamped with, verified before an action runs.
    Value: '%s'
`, stateTableResource(), artifactBucketResource(), assetBucketResource(), varsResources(ClassProduction), imageOptimizerResources(code.optimizer), tagPublisherResources(code.publisher, ClassProduction), edgeUserResource(EdgeUserName, trust, code.optimizer), outputStateBucket, stateTableOutput(), artifactBucketOutput(), assetBucketOutput(), varsOutputs(), imageOptimizerOutput(code.optimizer), outputVersion, version, outputInfraClass, ClassProduction)
}

// previewStackTemplate renders the preview infrastructure CloudFormation
// template. It shares the state bucket + state table shape production uses
// (each preview is its own Pulumi stack and needs the shared backend) and
// stamps InfrastructureClass=preview so a command can verify the substrate.
//
// The shared serverless cluster and the shared VPC/subnets/security-group/
// logging/execution-role scaffolding the PRD calls for are the opt-in-e2e seam:
// their correct shape and settling can only be validated against a live
// account, so — like RunPreview itself — they are added and exercised there.
// The stamped class, the shared backend, and the stack's independent lifecycle
// are settled here.
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
%s%s%s%s%s%s%sOutputs:
  %s:
    Description: S3 bucket holding Pulumi state for preview stacks.
    Value: !Ref StateBucket
%s%s%s%s%s  %s:
    Description: Ocel bootstrap schema version.
    Value: '%d'
  %s:
    Description: Class this substrate is stamped with, verified before an action runs.
    Value: '%s'
`, stateTableResource(), artifactBucketResource(), assetBucketResource(), varsResources(ClassPreview), imageOptimizerResources(code.optimizer), tagPublisherResources(code.publisher, ClassPreview), edgeUserResource(EdgePreviewUserName, trust, code.optimizer), outputStateBucket, stateTableOutput(), artifactBucketOutput(), assetBucketOutput(), varsOutputs(), imageOptimizerOutput(code.optimizer), outputVersion, version, outputInfraClass, ClassPreview)
}

// stateTableResource renders the StateTable resource block shared by both
// substrate templates: the account-global table every Ocel state entity is
// keyed into. Its pk/sk pair is deliberately opaque — upload sessions and Next
// ISR tag records already share it — so each entity namespaces itself with its
// own key prefix rather than getting a table of its own. expires_at is the TTL
// attribute; entities that outlive a request simply omit it. The block is a
// Resources child, so it is emitted before the template's Outputs: line.
//
// The gsi1pk/gsi1sk index is the time-ordered access path Next's tag sync
// needs: without it, "which tags changed since I last looked" is a scan of an
// account-global table. It is sparse — DynamoDB indexes only items carrying
// both index keys — so upload sessions and the ISR handler's own tag records,
// which write neither, stay out of it entirely. The projection carries every
// field a sync reads so one query answers it with no follow-up per-tag read.
//
// The stream carries NEW_IMAGE because a tag write IS the raise: the publisher
// that consumes it (publisher.go) needs the item's gsi1pk to learn which build
// the record belongs to and its watermarks to publish, and none of those are
// keys. It is the whole table's stream, not one entity's — DynamoDB has no
// finer grain — so every writer to this table is streamed and the publisher's
// event filter is what confines it to the TAG# partitions.
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

// stateTableOutput renders the StateTable name output shared by both substrate
// templates, consumed by the deploy path to address the table.
func stateTableOutput() string {
	return fmt.Sprintf(`  %s:
    Description: DynamoDB table holding account-global Ocel state, keyed by pk/sk.
    Value: !Ref StateTable
`, outputStateTable)
}

// artifactBucketResource renders the ArtifactBucket resource block shared by
// both substrate templates: the dedicated bucket function deployment artifacts
// are uploaded to. It carries the same public-access lockdown the state bucket
// uses, but no versioning (artifacts are content-addressed and immutable) and a
// lifecycle rule that expires artifacts (and aborts stale multipart uploads) to
// cap storage cost. The block is a Resources child, so it is emitted before the
// template's Outputs: line.
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

// artifactBucketOutput renders the ArtifactBucket name output shared by both
// substrate templates, consumed by the deploy path to address the bucket.
func artifactBucketOutput() string {
	return fmt.Sprintf(`  %s:
    Description: S3 bucket holding function deployment artifacts.
    Value: !Ref ArtifactBucket
`, outputArtifactBucket)
}

// assetBucketResource renders the AssetBucket resource block shared by both
// substrate templates: the dedicated bucket prerender configs + fallbacks are
// uploaded to, keyed by build id. It carries the same public-access lockdown
// and encryption the other buckets use and no versioning (keys are immutable
// per build), but — unlike the artifact bucket — NO object-expiration rule: a
// live build's assets are never re-uploaded by later deploys, so an age rule
// would delete assets still backing production. Superseded build prefixes are
// reaped by the deploy path instead. The block is a Resources child, so it is
// emitted before the template's Outputs: line.
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

// assetBucketOutput renders the AssetBucket name output shared by both substrate
// templates, consumed by the deploy path to address the bucket.
func assetBucketOutput() string {
	return fmt.Sprintf(`  %s:
    Description: S3 bucket holding prerender configs and fallbacks, keyed by build id.
    Value: !Ref AssetBucket
`, outputAssetBucket)
}

// edgeUserResource renders the per-substrate edge IAM user shared by both
// substrate templates: the single principal the Cloudflare worker signs its
// requests with. It carries an inline policy scoped to exactly the account-global
// stores this stack provisions, mirroring the Lambda tier's isrPolicy (cloud/aws
// deploy, a separate Go module) narrowed to what the edge itself calls.
//
// Reads are bucket-wide (s3:GetObject) but writes are not: the edge's fetch-cache
// writes are confined to the fetch-cache path segment of any prefix, because this
// key is long-lived and lives in Cloudflare's environment, where a bucket-wide
// write would let a compromise overwrite static assets, ISR entries and edge
// bundles. The prefix itself varies per app and build (<env>/<project>/<app>/
// <build>), so the wildcard admits any prefix and pins only the segment.
//
// The DynamoDB grants are bounded to the TAG# tag partitions so the edge key can
// never read or write the upload-session items (which carry HMAC secrets) sharing
// the table. Query is granted on the table's index separately: an index is not
// covered by its table's ARN, so the tag drain would otherwise 403.
//
// The lambda:Invoke* grant is what authorizes the worker's signed Function-URL
// forwards (the Lambdas are provisioned with AWS_IAM auth). It cannot be scoped
// by name — functions are Pulumi-autonamed, not prefixed — so it is scoped by
// attribute instead: any function carrying an ocel:app tag, which every deployed
// function does and nothing else in the account is expected to. Both actions are
// required since AWS's October 2025 change, and both honor aws:ResourceTag.
//
// The image optimizer belongs to no app and therefore carries no such tag, so it
// gets a statement of its own naming just that function — see
// imageOptimizerInvokeStatement for why not the two shortcuts.
//
// userName is the deterministic name the imperative access-key step
// (ensureEdgeCredentials, which also carries the credential rotation runbook)
// looks the user up by. The block is a Resources child, so it is emitted before
// the template's Outputs: line.
//
// It renders nothing for an edge inside the provider's trust boundary: such an
// edge reads under the provider's native identity, so a user whose only purpose
// is to hold a long-lived key would be a dangling credential in the account.
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
                Resource: !Sub '${AssetBucket.Arn}/*/fetch-cache/*'
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
%s`, userName, StateTableIndexName, imageOptimizerInvokeStatement(optimizer))
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
