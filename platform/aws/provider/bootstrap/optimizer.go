package bootstrap

import (
	"context"
	"fmt"
)

const (
	outputImageOptimizerURL = "ImageOptimizerFunctionUrl"

	optimizerAssetName = "image-optimizer.zip"

	optimizerKeyPrefix = "ocel-image-optimizer"

	optimizerRuntime      = "nodejs22.x"
	optimizerArchitecture = "arm64"
	optimizerHandler      = "index.handler"
	optimizerMemoryMB     = 1769

	optimizerTimeoutSeconds = 20

	optimizerThreadpoolSize = 4

	optimizerBucketEnvVar = "OCEL_IMAGE_ASSET_BUCKET"
)

const optimizerLabel = "image optimizer"

func pinnedOptimizer() artifactPin {
	return artifactPin{version: ImageOptimizerArtifactVersion, sha256: ImageOptimizerArtifactSHA256}
}

func optimizerReleaseURL(version string) string {
	return fmt.Sprintf("https://github.com/ocelhq/ocel/releases/download/image-optimizer-v%s/%s", version, optimizerAssetName)
}

func optimizerArtifactKey(p artifactPin) string {
	return fmt.Sprintf("%s/%s-%s.zip", optimizerKeyPrefix, p.version, p.digest())
}

func ensureOptimizerArtifact(ctx context.Context, art Artifacts, bucket string, p artifactPin) (artifactCode, error) {
	return ensureArtifact(ctx, art, bucket, optimizerArtifactKey(p), optimizerReleaseURL(p.version), optimizerLabel, p)
}

func imageOptimizerResources(code artifactCode) string {
	if !code.present() {
		return ""
	}
	return fmt.Sprintf(`  ImageOptimizerRole:
    Type: AWS::IAM::Role
    Properties:
      Description: "Execution role for this substrate's shared image optimizer. Grants read on the asset bucket's assets and image-config prefixes and nothing else, so a compromised optimizer cannot reach state, variables or another app's data. Managed by ocel bootstrap; deleting it breaks /_next/image for every app in this substrate."
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
      Description: "Ocel image optimizer - one shared function transforming /_next/image for every app in this substrate, invoked by the edge. Managed by ocel bootstrap; delete it and /_next/image answers 502 everywhere here."
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
    Metadata:
      Description: "The edge's entry point to the image optimizer, IAM-authenticated so only the edge user can call it and response-streaming so a large image does not buffer. Its URL is a stack output the edge is given at bootstrap; recreate this and the edge keeps calling the old URL until bootstrap runs again."
    Properties:
      TargetFunctionArn: !GetAtt ImageOptimizer.Arn
      AuthType: AWS_IAM
      InvokeMode: RESPONSE_STREAM
`, optimizerRuntime, optimizerArchitecture, optimizerHandler, optimizerMemoryMB, optimizerTimeoutSeconds,
		code.bucket, code.key, optimizerBucketEnvVar, optimizerThreadpoolSize)
}

func imageOptimizerOutput(code artifactCode) string {
	if !code.present() {
		return ""
	}
	return fmt.Sprintf(`  %s:
    Description: "Function URL of this substrate's shared image optimizer. The edge calls it with requests signed by the edge user."
    Value: !GetAtt ImageOptimizerUrl.FunctionUrl
`, outputImageOptimizerURL)
}

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
