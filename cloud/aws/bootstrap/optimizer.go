package bootstrap

import (
	"context"
	"fmt"
)

// The account-global image optimizer: the Lambda every /_next/image request in a
// substrate is transformed by, and the distribution path that gets its artifact
// into the customer's own account.
//
// One optimizer per substrate, not per account. Each bootstrap stack carries its
// own AssetBucket and its own edge IAM user, and the optimizer reads originals
// and image configs out of that bucket — so a single shared function would need
// read access to both substrates' buckets, which is exactly the isolation the
// split buckets exist to provide. Production's reads only production's bucket
// and is invoked only by production's edge user; preview's likewise.

const (
	// outputImageOptimizerURL publishes the Function URL the deploy path binds
	// into every worker as OCEL_IMAGE_OPTIMIZER_URL. Absent from a stack that
	// rendered no optimizer, which leaves the worker's image origin unbound.
	outputImageOptimizerURL = "ImageOptimizerFunctionUrl"

	// optimizerAssetName is the file name the release asset is published under.
	// It matches what packages/image-optimizer/scripts/build-zip.mjs writes.
	optimizerAssetName = "image-optimizer.zip"

	// optimizerKeyPrefix is where the artifact lands in the account's artifact
	// bucket. Function artifacts there are keyed `<slug>/<function>/<hash>.zip`
	// — three segments — so no function artifact can ever land on this
	// two-segment key. The prefix is not reserved, though: destroying a project
	// slugged `ocel-image-optimizer` deletes `<slug>/` recursively and takes this
	// object with it. That costs nothing beyond a re-download, because a running
	// Lambda holds its own copy of the code and the next bootstrap re-uploads
	// what is missing — the same thing the bucket's own expiration rule does.
	optimizerKeyPrefix = "ocel-image-optimizer"

	// optimizerRuntime, optimizerArchitecture, optimizerHandler and
	// optimizerMemoryMB are what the artifact is built for and needs. arm64 is
	// the architecture build-zip.mjs cross-installs sharp's native binaries for,
	// so x86_64 would fail to load the addon at all. 1769 MB is the first size
	// AWS gives a full vCPU, and libvips is genuinely multi-threaded.
	optimizerRuntime      = "nodejs22.x"
	optimizerArchitecture = "arm64"
	optimizerHandler      = "index.handler"
	optimizerMemoryMB     = 1769

	// optimizerTimeoutSeconds must exceed the sum of the deadlines the artifact
	// enforces internally, or Lambda kills the invocation before the function can
	// answer with the error it was about to: a 7 s upstream wall clock plus a 7 s
	// libvips timeout can run back to back, and the S3 config read precedes both.
	// Overshooting costs nothing — nothing waits out this budget on a healthy
	// request — while undershooting turns every slow origin into an opaque 502.
	optimizerTimeoutSeconds = 20

	// optimizerThreadpoolSize pins libuv's pool, which is where sharp does its
	// work. The artifact also sets it in-process, but that is a no-op at libuv's
	// default of 4 and cannot raise or lower the pool once it exists — the
	// function configuration is the setting that actually binds. Peak memory
	// scales with this times sharp.concurrency().
	optimizerThreadpoolSize = 4

	// optimizerBucketEnvVar names the asset bucket to the function. The artifact
	// refuses to start without it.
	optimizerBucketEnvVar = "OCEL_IMAGE_ASSET_BUCKET"
)

// optimizerLabel names the artifact in every message the placement path can
// fail with.
const optimizerLabel = "image optimizer"

func pinnedOptimizer() artifactPin {
	return artifactPin{version: ImageOptimizerArtifactVersion, sha256: ImageOptimizerArtifactSHA256}
}

// optimizerReleaseURL is where the pinned asset is published. The tag is the
// contract the release step must satisfy; nothing discovers it.
func optimizerReleaseURL(version string) string {
	return fmt.Sprintf("https://github.com/ocelhq/ocel/releases/download/image-optimizer-v%s/%s", version, optimizerAssetName)
}

// optimizerArtifactKey is content-addressed on the pinned digest, not just the
// version: a digest that moves lands at a new key, which is what makes
// CloudFormation see a code change and update the function. Keying on the
// version alone would let a re-published asset be ignored.
func optimizerArtifactKey(p artifactPin) string {
	return fmt.Sprintf("%s/%s-%s.zip", optimizerKeyPrefix, p.version, p.digest())
}

// ensureOptimizerArtifact places the pinned optimizer zip; see ensureArtifact
// for the fail-closed discipline it runs under.
func ensureOptimizerArtifact(ctx context.Context, art Artifacts, bucket string, p artifactPin) (artifactCode, error) {
	return ensureArtifact(ctx, art, bucket, optimizerArtifactKey(p), optimizerReleaseURL(p.version), optimizerLabel, p)
}

// imageOptimizerResources renders the optimizer's execution role, function and
// Function URL, or nothing when no artifact is available.
//
// The role reads exactly the two prefixes the artifact reads — assets/ for the
// original image and image-config/ for the compiled validation config — and
// writes nothing anywhere. It is scoped to this substrate's own AssetBucket, so
// production's optimizer cannot see preview's bytes or the reverse.
//
// The function is deliberately unnamed: CloudFormation autonames it, and every
// grant against it is by !GetAtt rather than by a name something might guess or
// collide with. Nothing tags it ocel:app either — it belongs to no app, and a
// fabricated tag would both be a lie and make everything keyed off that tag
// misclassify this function.
func imageOptimizerResources(code artifactCode) string {
	if !code.present() {
		return ""
	}
	return fmt.Sprintf(`  ImageOptimizerRole:
    Type: AWS::IAM::Role
    Properties:
      AssumeRolePolicyDocument:
        Version: '2012-10-17'
        Statement:
          - Effect: Allow
            Principal:
              Service: lambda.amazonaws.com
            Action: sts:AssumeRole
      ManagedPolicyArns:
        - arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole
      Policies:
        - PolicyName: ocel-image-optimizer-read
          PolicyDocument:
            Version: '2012-10-17'
            Statement:
              - Effect: Allow
                Action: s3:GetObject
                Resource:
                  - !Sub '${AssetBucket.Arn}/assets/*'
                  - !Sub '${AssetBucket.Arn}/image-config/*'
  ImageOptimizer:
    Type: AWS::Lambda::Function
    Properties:
      Description: Ocel image optimizer - transforms /_next/image requests for every app in this substrate.
      Runtime: %s
      Architectures:
        - %s
      Handler: %s
      MemorySize: %d
      Timeout: %d
      Role: !GetAtt ImageOptimizerRole.Arn
      Code:
        S3Bucket: %s
        S3Key: %s
      Environment:
        Variables:
          %s: !Ref AssetBucket
          UV_THREADPOOL_SIZE: '%d'
  ImageOptimizerUrl:
    Type: AWS::Lambda::Url
    Properties:
      TargetFunctionArn: !GetAtt ImageOptimizer.Arn
      AuthType: AWS_IAM
      InvokeMode: RESPONSE_STREAM
`, optimizerRuntime, optimizerArchitecture, optimizerHandler, optimizerMemoryMB, optimizerTimeoutSeconds,
		code.bucket, code.key, optimizerBucketEnvVar, optimizerThreadpoolSize)
}

// imageOptimizerOutput publishes the Function URL, or nothing when no optimizer
// was rendered — an absent output is what leaves the worker's image origin
// unbound rather than bound to an empty string.
func imageOptimizerOutput(code artifactCode) string {
	if !code.present() {
		return ""
	}
	return fmt.Sprintf(`  %s:
    Description: Function URL of the substrate's image optimizer, signed by the edge user.
    Value: !GetAtt ImageOptimizerUrl.FunctionUrl
`, outputImageOptimizerURL)
}

// imageOptimizerInvokeStatement renders the edge user's Allow on the optimizer,
// or nothing when none was rendered.
//
// It is a statement of its own, naming this one function, because the edge user's
// general Lambda-invoke grant is conditioned on the ocel:app tag and the
// optimizer carries no such tag — as things stood the edge could not invoke it at
// all. The two alternatives were both worse: dropping the tag condition would let
// the edge key invoke every Lambda in the customer's account, and tagging the
// optimizer ocel:app would lie about which app owns it. This way the edge gains
// exactly one new named callable target and every app function stays governed by
// the tag.
//
// An edge inside the provider's trust boundary has no edge user to carry this at
// all (edgeUserResource renders nothing for one), so such a substrate gets an
// AWS_IAM optimizer with no in-account principal allowed to invoke it. Internal
// trust is not a live path today; the edge it describes would need this same
// named grant against its own identity before it were one.
func imageOptimizerInvokeStatement(code artifactCode) string {
	if !code.present() {
		return ""
	}
	return `              - Effect: Allow
                Action:
                  - lambda:InvokeFunctionUrl
                  - lambda:InvokeFunction
                Resource: !GetAtt ImageOptimizer.Arn
`
}
