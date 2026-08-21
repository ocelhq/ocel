package bootstrap

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const (
	outputImageOptimizerURL = "ImageOptimizerFunctionUrl"
	outputImageOptimizerARN = "ImageOptimizerArn"

	optimizerKeyPrefix = "ocel-image-optimizer"

	optimizerRuntime      = "nodejs22.x"
	optimizerArchitecture = "arm64"
	optimizerHandler      = "index.handler"
	optimizerMemoryMB     = 1769

	optimizerTimeoutSeconds = 20

	optimizerThreadpoolSize = 4

	optimizerBucketEnvVar = "OCEL_IMAGE_ASSET_BUCKET"

	optimizerComponentTagKey   = "ocel:component"
	optimizerComponentTagValue = "image-optimizer"
)

const optimizerLabel = "image optimizer"

func optimizerPlacement(bucket string) payloads.Placement {
	return payloads.At(bucket, optimizerKeyPrefix, payloads.ImageOptimizer())
}

func ensureOptimizerPayload(ctx context.Context, store ObjectStore, bucket string) (payloads.Placement, error) {
	return payloads.Place(ctx, store, bucket, optimizerKeyPrefix, optimizerLabel, payloads.ImageOptimizer())
}

func imageOptimizerResources(code payloads.Placement) string {
	return fmt.Sprintf(`  ImageOptimizerRole:
    Type: AWS::IAM::Role
    Properties:
      Description: "Execution role for this bootstrap's shared image optimizer. Grants read on the asset bucket's assets and image-config prefixes and nothing else, so a compromised optimizer cannot reach state, variables or another app's data. Managed by ocel bootstrap; deleting it breaks /_next/image for every app in this bootstrap."
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
                  - !Sub '${AssetBucketArn}/*/assets/*'
                  - !Sub '${AssetBucketArn}/*/image-config.json'
  ImageOptimizer:
    Type: AWS::Lambda::Function
    Properties:
      Description: "Ocel image optimizer - one shared function transforming /_next/image for every app in this bootstrap, invoked by the edge. Managed by ocel bootstrap; delete it and /_next/image answers 502 everywhere here."
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
          %s: !Ref AssetBucketName
          UV_THREADPOOL_SIZE: '%d'
      Tags:
        - Key: %s
          Value: %s
  ImageOptimizerUrl:
    Type: AWS::Lambda::Url
    Metadata:
      Description: "The edge's entry point to the image optimizer, IAM-authenticated so only the edge user can call it and response-streaming so a large image does not buffer. Its URL is a stack output the edge is given at bootstrap; recreate this and the edge keeps calling the old URL until bootstrap runs again."
    Properties:
      TargetFunctionArn: !GetAtt ImageOptimizer.Arn
      AuthType: AWS_IAM
      InvokeMode: RESPONSE_STREAM
`, optimizerRuntime, optimizerArchitecture, optimizerHandler, optimizerMemoryMB, optimizerTimeoutSeconds,
		code.Bucket, code.Key, optimizerBucketEnvVar, optimizerThreadpoolSize,
		optimizerComponentTagKey, optimizerComponentTagValue)
}

func imageOptimizerOutputs() string {
	return fmt.Sprintf(`  %s:
    Description: "Function URL of this bootstrap's shared image optimizer. The edge calls it with requests signed by the edge user."
    Value: !GetAtt ImageOptimizerUrl.FunctionUrl
  %s:
    Description: "ARN of this bootstrap's shared image optimizer, handed to whichever feature stack has to grant invoke on it."
    Value: !GetAtt ImageOptimizer.Arn
`, outputImageOptimizerURL, outputImageOptimizerARN)
}

func imageOptimizerInvokeStatement() string {
	return `              - Effect: Allow
                Action:
                  - lambda:InvokeFunctionUrl
                  - lambda:InvokeFunction
                Resource: !Ref ImageOptimizerArn
`
}
