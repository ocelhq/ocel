package bootstrap

import (
	"context"
	"fmt"

	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

var cloudflareEdgeFeature = feature{
	name:       FeatureCloudflareEdge,
	summary:    "Cloudflare as the front — workers, credential, snapshot publisher",
	dependsOn:  []string{FeatureISR},
	needs:      []string{needsEdgePrefix + "cloudflare"},
	template:   cloudflareEdgeTemplate,
	payloads:   cloudflareEdgePayloads,
	placements: cloudflareEdgePlacements,
	before:     adoptCloudflareEdge,
	after:      mintEdgeCredentials,
	drop:       severCloudflareEdge,
}

var cloudflareEdge = func() edge.Edge { return cloudflare.New() }

func cloudflareEdgePayloads(ctx context.Context, store ObjectStore, bucket string) (stackPayloads, error) {
	var code stackPayloads
	var err error
	code.publisher, err = ensureTagPublisherPayload(ctx, store, bucket)
	return code, err
}

func cloudflareEdgePlacements(bucket string) stackPayloads {
	return stackPayloads{publisher: tagPublisherPlacement(bucket)}
}

func cloudflareEdgeTemplate(in featureInputs) featureStack {
	specs := []crossStackParam{
		{paramAssetBucketName, "The core bootstrap's asset bucket, which the edge reads static assets from and writes its fetch cache back into.", in.refs.assetBucket},
		{paramAssetBucketARN, "ARN of that bucket, so the edge reader is granted the asset and fetch-cache prefixes and nothing else.", in.refs.assetBucketARN},
		{paramStateTableARN, "ARN of the core bootstrap's state table, so the edge reader reaches tag items alone.", in.refs.stateTableARN},
		{paramStateTableStreamARN, "ARN of that table's stream, the only trigger the tag publisher has.", in.refs.stateTableStreamARN},
		{paramRevalidateQueueARN, "ARN of the revalidation queue the ISR feature stood up, the one queue the edge reader may enqueue a refresh on.", in.refs.revalidateQueueARN},
	}
	optimizer := in.alongside.Has(FeatureImageOptimization)
	if optimizer {
		specs = append(specs, crossStackParam{paramImageOptimizerARN, "ARN of the shared image optimizer, the one function the edge reader may invoke for an image.", in.refs.imageOptimizerARN})
	}
	params, values := crossStack(specs)

	userName := EdgeUserName
	if in.class == ClassPreview {
		userName = EdgePreviewUserName
	}
	return featureStack{
		params: values,
		body: fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: "Ocel bootstrap feature (%s, %s) - what a Cloudflare front needs inside this AWS account: the IAM user it signs its calls with, scoped to this bootstrap alone, and the publisher that carries each build's tag snapshot out to the edge's ISR writer."
%sResources:
%s%s`,
			FeatureCloudflareEdge, in.class, params,
			edgeUserResource(userName, in.class, optimizer),
			tagPublisherResources(in.code.publisher, in.class)),
	}
}

func edgeUserResource(userName, class string, optimizer bool) string {
	invoke := ""
	if optimizer {
		invoke = imageOptimizerInvokeStatement()
	}
	return fmt.Sprintf(`  EdgeUser:
    Type: AWS::IAM::User
    Metadata:
      Description: "The identity the %s edge signs its calls into this account with: it reads the asset bucket, writes the fetch cache back, reads and updates tag items, invokes app functions and enqueues ISR revalidations."
    Properties:
      UserName: %s
      Policies:
        - PolicyName: ocel-edge-cache
          PolicyDocument:
            Version: '2012-10-17'
            Statement:
              - Effect: Allow
                Action: s3:GetObject
                Resource: !Sub '${%s}/*'
              - Effect: Allow
                Action: s3:PutObject
                Resource: !Sub '${%s}/*/fetch-cache/*.cache.json'
              - Effect: Allow
                Action:
                  - dynamodb:BatchGetItem
                  - dynamodb:UpdateItem
                Resource: !Ref %s
                Condition:
                  ForAllValues:StringLike:
                    dynamodb:LeadingKeys:
                      - 'PROJECT#*#TAG#*'
              - Effect: Allow
                Action: dynamodb:Query
                Resource: !Sub '${%s}/index/%s'
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
                Resource: !Ref %s
              - Effect: Allow
                Action:
                  - kms:GenerateDataKey
                  - kms:Decrypt
                Resource: '*'
                Condition:
                  StringEquals:
                    kms:ViaService: !Sub 'sqs.${AWS::Region}.amazonaws.com'
%s`, class, userName,
		paramAssetBucketARN, paramAssetBucketARN,
		paramStateTableARN, paramStateTableARN, StateTableIndexName,
		paramRevalidateQueueARN, invoke)
}

func adoptCloudflareEdge(ctx context.Context, d stepDeps) error {
	return bootstrapEdge(ctx, d, cloudflareEdge())
}

func severCloudflareEdge(ctx context.Context, d stepDeps) error {
	names, err := edgeNamesFor(d.class)
	if err != nil {
		return err
	}
	d.progress(fmt.Sprintf("Deleting the access key of edge reader %s", names.user))
	if err := deleteAccessKeys(ctx, d.iam, names.user); err != nil {
		return err
	}
	d.progress("Deleting what the edge was reached through (SSM)")
	for _, param := range names.edgeParams() {
		if err := deleteParam(ctx, d.ssm, param); err != nil {
			return err
		}
	}
	return nil
}

func mintEdgeCredentials(ctx context.Context, d stepDeps) error {
	d.progress("Ensuring edge reader credentials (SSM SecureString)")
	created, err := ensureEdgeCredentials(ctx, d.iam, d.ssm, d.class)
	if err != nil {
		return err
	}
	if created {
		d.log("minted a new edge reader access key")
	} else {
		d.log("reused the existing edge reader access key")
	}
	return nil
}
