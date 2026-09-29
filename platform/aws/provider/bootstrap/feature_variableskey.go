package bootstrap

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

var variablesKeyFeature = feature{
	name:       provider.FeatureVariablesKey,
	summary:    "a KMS key to encrypt variables under, the one bootstrap item with a recurring cost, about $1 a month prorated hourly, and the sync that keeps scheduled env sources current",
	template:   variablesKeyTemplate,
	payloads:   variablesKeyPayloads,
	placements: variablesKeyPlacements,
	afterPlan:  validateBroughtKey,
}

func variablesKeyPayloads(ctx context.Context, store ObjectStore, bucket string) (stackPayloads, error) {
	code, err := ensureEnvSourceSyncPayload(ctx, store, bucket)
	return stackPayloads{envSourceSync: code}, err
}

func variablesKeyPlacements(bucket string) stackPayloads {
	return stackPayloads{envSourceSync: envSourceSyncPlacement(bucket)}
}

func variablesKeyTemplate(in featureInputs) featureStack {
	params, values := crossStack([]crossStackParam{
		{paramVariablesTableName, "The core bootstrap's variables table, which the env source sync reads registrations from and writes values into.", in.refs.variablesTable},
		{paramVariablesTableARN, "ARN of that table, so the env source sync's role reaches this table and no other.", in.refs.variablesTableARN},
	})
	if in.variablesKey != "" {
		return featureStack{params: values, body: fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: "Ocel bootstrap feature (%s, %s) - the record of the KMS key this account brought, which every encrypted variable of this tier is sealed under, and the env source sync that encrypts under it. Ocel owns no key here and puts nothing on it."
%sResources:
  VariablesKeyRecord:
    Type: AWS::CloudFormation::WaitConditionHandle
    Metadata:
      Description: "Placeholder for the %s tier's brought variable key: the stack records the key ARN as an output and creates nothing. The app boundary admits the key by that ARN."
%sOutputs:
%s`,
			provider.FeatureVariablesKey, in.tier, params, in.tier,
			envSourceSyncResources(in.ns, in.code.envSourceSync, in.tier, fmt.Sprintf("%q", in.variablesKey)),
			broughtVariablesKeyOutput(in.variablesKey))}
	}
	return featureStack{
		params: values,
		body: fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: "Ocel bootstrap feature (%s, %s) - the KMS key every encrypted variable of this tier is sealed under, the alias naming it, and the env source sync that encrypts under it."
%sResources:
%s%sOutputs:
%s`,
			provider.FeatureVariablesKey, in.tier, params,
			variablesKeyResources(in.ns, in.tier),
			envSourceSyncResources(in.ns, in.code.envSourceSync, in.tier, "!GetAtt VariablesKey.Arn"),
			variablesKeyOutputs()),
	}
}

func broughtVariablesKeyOutput(named string) string {
	return fmt.Sprintf(`  %s:
    Description: "KMS key every encrypted value of this tier is encrypted under. This account brought it, so ocel records it and neither made nor manages it."
    Value: %q
  %s:
    Description: "Marks the key above as one this account brought, so a later run knows ocel made no key here."
    Value: "true"
`, outputVariablesKeyARN, named, outputVariablesKeyBrought)
}

type KeyAPI interface {
	DescribeKey(ctx context.Context, in *kms.DescribeKeyInput, optFns ...func(*kms.Options)) (*kms.DescribeKeyOutput, error)
	Encrypt(ctx context.Context, in *kms.EncryptInput, optFns ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(ctx context.Context, in *kms.DecryptInput, optFns ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

const variablesKeyProbeBytes = 16

func validateBroughtKey(ctx context.Context, apis ParamAPIs, ns Namespace, tier environment.Tier, req Request) ([]provider.Change, error) {
	if req.VariablesKey == "" {
		return nil, nil
	}
	described, err := apis.KMS.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(req.VariablesKey)})
	if err != nil {
		return nil, refuseBroughtKey(req.VariablesKey, "it could not be described: %v", err)
	}
	metadata := described.KeyMetadata
	named := aws.ToString(metadata.Arn)
	region, err := keyRegion(named)
	switch {
	case metadata.KeySpec != kmstypes.KeySpecSymmetricDefault || metadata.KeyUsage != kmstypes.KeyUsageTypeEncryptDecrypt:
		return nil, refuseBroughtKey(req.VariablesKey,
			"it is a %s key for %s, and a variable is sealed under a symmetric ENCRYPT_DECRYPT key", metadata.KeySpec, metadata.KeyUsage)
	case !metadata.Enabled:
		return nil, refuseBroughtKey(req.VariablesKey, "it is not enabled, and a key that is not enabled seals and opens nothing")
	case err != nil:
		return nil, refuseBroughtKey(req.VariablesKey, "%v", err)
	case region != apis.Region:
		return nil, refuseBroughtKey(req.VariablesKey,
			"it lives in region %s and this bootstrap is in region %s, which a function reading a value never reaches",
			region, apis.Region)
	}

	probe := make([]byte, variablesKeyProbeBytes)
	if _, err := rand.Read(probe); err != nil {
		return nil, fmt.Errorf("generate a probe for the brought variable key: %w", err)
	}
	bound := map[string]string{"ocel:probe": string(tier)}
	sealed, err := apis.KMS.Encrypt(ctx, &kms.EncryptInput{
		KeyId:             aws.String(req.VariablesKey),
		Plaintext:         probe,
		EncryptionContext: bound,
	})
	if err != nil {
		return nil, refuseBroughtKey(req.VariablesKey, "nothing could be encrypted under it: %v", err)
	}
	opened, err := apis.KMS.Decrypt(ctx, &kms.DecryptInput{
		KeyId:             aws.String(req.VariablesKey),
		CiphertextBlob:    sealed.CiphertextBlob,
		EncryptionContext: bound,
	})
	if err != nil {
		return nil, refuseBroughtKey(req.VariablesKey, "what was encrypted under it could not be decrypted again: %v", err)
	}
	if !bytes.Equal(opened.Plaintext, probe) {
		return nil, refuseBroughtKey(req.VariablesKey, "what it decrypted is not what was encrypted under it")
	}
	return nil, nil
}

func refuseBroughtKey(named, why string, args ...any) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"%s cannot seal this account's variables: %s.\nIts key policy must admit this principal, the app execution roles that read a value and the env source sync that writes one; ocel never edits a key policy it does not own",
		named, fmt.Sprintf(why, args...))
}

func keyRegion(named string) (string, error) {
	parsed, err := arn.Parse(named)
	if err != nil || parsed.Service != "kms" || parsed.Region == "" {
		return "", fmt.Errorf("%q is not a key ARN this account can be pointed at, so nothing can tell which region the key lives in", named)
	}
	return parsed.Region, nil
}
