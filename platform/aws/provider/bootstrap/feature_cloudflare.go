package bootstrap

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const KindCloudflare = "cloudflare"

var cloudflareEdgeFeature = feature{
	name:       FeatureCloudflareEdge,
	summary:    "Cloudflare as the front — workers, credential, snapshot publisher",
	dependsOn:  []string{FeatureISR},
	needs:      []string{needsEdgePrefix + KindCloudflare},
	template:   cloudflareEdgeTemplate,
	payloads:   cloudflareEdgePayloads,
	placements: cloudflareEdgePlacements,
	after:      mintEdgeCredentials,
	drop:       severCloudflareEdge,
}

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

func plannedEdgeCredentials(ctx context.Context, apis ParamAPIs, class string, req Request) ([]providerkit.Change, error) {
	if !slices.Contains(req.Features, FeatureCloudflareEdge) {
		return nil, nil
	}
	names, err := edgeNamesFor(class, KindCloudflare)
	if err != nil {
		return nil, err
	}
	recorded, held, err := recordedEdgeKeyID(ctx, apis.SSM, names.credentialsParam)
	if err != nil {
		return nil, err
	}
	standing, err := edgeKeyStanding(ctx, apis.IAM, names.user, recorded)
	if err != nil {
		return nil, err
	}
	if standing {
		return []providerkit.Change{
			{Kind: kindParameter, Name: names.credentialsParam, Action: providerkit.ActionKeep, Reason: paramCurrent},
			{Kind: kindAccessKey, Name: names.user, Action: providerkit.ActionKeep, Reason: paramCurrent},
		}, nil
	}
	credentials := providerkit.Change{Kind: kindParameter, Name: names.credentialsParam, Action: providerkit.ActionCreate}
	if held {
		credentials.Action, credentials.Reason = providerkit.ActionUpdate, keyGone
		if recorded == "" {
			credentials.Reason = keyUnrecorded
		}
	}
	return []providerkit.Change{
		credentials,
		{Kind: kindAccessKey, Name: names.user, Action: providerkit.ActionCreate},
	}, nil
}

func plannedCloudflareSever(ctx context.Context, apis ParamAPIs, class string, req Request) ([]providerkit.Change, error) {
	if !slices.Contains(req.Remove, FeatureCloudflareEdge) || slices.Contains(req.Features, FeatureCloudflareEdge) {
		return nil, nil
	}
	names, err := edgeNamesFor(class, KindCloudflare)
	if err != nil {
		return nil, err
	}
	reason := fmt.Sprintf(severedByRemove, FeatureCloudflareEdge, KindCloudflare)

	var changes []providerkit.Change
	keys, err := liveAccessKeys(ctx, apis.IAM, names.user)
	if err != nil {
		return nil, err
	}
	if len(keys) > 0 {
		changes = append(changes, providerkit.Change{
			Kind: kindAccessKey, Name: names.user, Action: providerkit.ActionDelete, Reason: reason,
		})
	}
	for _, param := range names.edgeParams() {
		held, err := paramHeld(ctx, apis.SSM, param)
		if err != nil {
			return nil, err
		}
		if !held {
			continue
		}
		changes = append(changes, providerkit.Change{
			Kind: kindParameter, Name: param, Action: providerkit.ActionDelete, Reason: reason,
		})
	}
	return changes, nil
}

func severCloudflareEdge(ctx context.Context, d stepDeps) error {
	names, err := edgeNamesFor(d.class, KindCloudflare)
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
	created, err := ensureEdgeCredentials(ctx, d.iam, d.ssm, d.class, KindCloudflare)
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
