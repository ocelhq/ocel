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

	"github.com/ocelhq/ocel/pkg/providerkit"
)

var varsKeyFeature = feature{
	name:      FeatureVarsKey,
	summary:   "a KMS key to encrypt variables under, the one bootstrap item with a standing cost, about $1 a month prorated hourly",
	template:  varsKeyTemplate,
	afterPlan: validateBroughtKey,
}

func varsKeyTemplate(in featureInputs) featureStack {
	if in.varsKey != "" {
		return featureStack{body: fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: "Ocel bootstrap feature (%s, %s) - the record of the KMS key this account brought, which every encrypted variable of this class is sealed under. Ocel owns no key here."
Resources:
  VarsKeyRecord:
    Type: AWS::CloudFormation::WaitConditionHandle
    Metadata:
      Description: "Placeholder for the %s class's brought variable key: the stack records the key ARN as an output and creates nothing."
Outputs:
%s`,
			FeatureVarsKey, in.class, in.class, broughtVarsKeyOutput(in.varsKey))}
	}
	return featureStack{
		body: fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: "Ocel bootstrap feature (%s, %s) - the KMS key every encrypted variable of this class is sealed under, and the alias naming it."
Resources:
%sOutputs:
%s`,
			FeatureVarsKey, in.class,
			varsKeyResources(in.class),
			varsKeyOutputs()),
	}
}

func broughtVarsKeyOutput(named string) string {
	return fmt.Sprintf(`  %s:
    Description: "KMS key every encrypted value of this class is encrypted under. This account brought it, so ocel records it and neither made nor manages it."
    Value: %q
  %s:
    Description: "Marks the key above as one this account brought, so a later run knows ocel made no key here."
    Value: "true"
`, outputVarsKeyARN, named, outputVarsKeyBrought)
}

type KeyAPI interface {
	DescribeKey(ctx context.Context, in *kms.DescribeKeyInput, optFns ...func(*kms.Options)) (*kms.DescribeKeyOutput, error)
	Encrypt(ctx context.Context, in *kms.EncryptInput, optFns ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(ctx context.Context, in *kms.DecryptInput, optFns ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

const varsKeyProbeBytes = 16

func validateBroughtKey(ctx context.Context, apis ParamAPIs, class string, req Request) ([]providerkit.Change, error) {
	if req.VarsKey == "" {
		return nil, nil
	}
	described, err := apis.KMS.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(req.VarsKey)})
	if err != nil {
		return nil, refuseBroughtKey(req.VarsKey, "it could not be described: %v", err)
	}
	held := described.KeyMetadata
	named := aws.ToString(held.Arn)
	region, err := keyRegion(named)
	switch {
	case held.KeySpec != kmstypes.KeySpecSymmetricDefault || held.KeyUsage != kmstypes.KeyUsageTypeEncryptDecrypt:
		return nil, refuseBroughtKey(req.VarsKey,
			"it is a %s key for %s, and a variable is sealed under a symmetric ENCRYPT_DECRYPT key", held.KeySpec, held.KeyUsage)
	case !held.Enabled:
		return nil, refuseBroughtKey(req.VarsKey, "it is not enabled, and a key that is not enabled seals and opens nothing")
	case err != nil:
		return nil, refuseBroughtKey(req.VarsKey, "%v", err)
	case region != apis.Region:
		return nil, refuseBroughtKey(req.VarsKey,
			"it lives in region %s and this bootstrap is in region %s, which a function reading a value never reaches",
			region, apis.Region)
	}

	probe := make([]byte, varsKeyProbeBytes)
	if _, err := rand.Read(probe); err != nil {
		return nil, fmt.Errorf("generate a probe for the brought variable key: %w", err)
	}
	bound := map[string]string{"ocel:probe": class}
	sealed, err := apis.KMS.Encrypt(ctx, &kms.EncryptInput{
		KeyId:             aws.String(req.VarsKey),
		Plaintext:         probe,
		EncryptionContext: bound,
	})
	if err != nil {
		return nil, refuseBroughtKey(req.VarsKey, "nothing could be encrypted under it: %v", err)
	}
	opened, err := apis.KMS.Decrypt(ctx, &kms.DecryptInput{
		KeyId:             aws.String(req.VarsKey),
		CiphertextBlob:    sealed.CiphertextBlob,
		EncryptionContext: bound,
	})
	if err != nil {
		return nil, refuseBroughtKey(req.VarsKey, "what was encrypted under it could not be decrypted again: %v", err)
	}
	if !bytes.Equal(opened.Plaintext, probe) {
		return nil, refuseBroughtKey(req.VarsKey, "what it decrypted is not what was encrypted under it")
	}
	return nil, nil
}

func refuseBroughtKey(named, why string, args ...any) error {
	return providerkit.Refuse(providerkit.CodeInvalid,
		"%s cannot hold this account's variables: %s.\nIts key policy must admit this principal and the app execution roles that read a value; ocel never edits a key policy it does not own",
		named, fmt.Sprintf(why, args...))
}

func keyRegion(named string) (string, error) {
	parsed, err := arn.Parse(named)
	if err != nil || parsed.Service != "kms" || parsed.Region == "" {
		return "", fmt.Errorf("%q is not a key ARN this account can be pointed at, so nothing can tell which region the key lives in", named)
	}
	return parsed.Region, nil
}
