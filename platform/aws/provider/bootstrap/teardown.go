package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	smithy "github.com/aws/smithy-go"
)

type CFNTeardownAPI interface {
	CFNDescriber
	DeleteStack(ctx context.Context, in *cloudformation.DeleteStackInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DeleteStackOutput, error)
}

type IAMKeyAPI interface {
	ListAccessKeys(ctx context.Context, in *iam.ListAccessKeysInput, optFns ...func(*iam.Options)) (*iam.ListAccessKeysOutput, error)
	DeleteAccessKey(ctx context.Context, in *iam.DeleteAccessKeyInput, optFns ...func(*iam.Options)) (*iam.DeleteAccessKeyOutput, error)
}

type BucketEmptierAPI interface {
	ListObjectVersions(ctx context.Context, in *s3.ListObjectVersionsInput, optFns ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error)
	DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

type TeardownAPIs struct {
	CFN     CFNTeardownAPI
	SSM     SSMAPI
	IAM     IAMKeyAPI
	Buckets BucketEmptierAPI
}

func StackNameFor(class string) (string, error) {
	switch class {
	case ClassProduction:
		return StackName, nil
	case ClassPreview:
		return PreviewStackName, nil
	default:
		return "", fmt.Errorf("bootstrap: unknown substrate class %q", class)
	}
}

func SiblingClassOf(class string) (string, error) {
	switch class {
	case ClassProduction:
		return ClassPreview, nil
	case ClassPreview:
		return ClassProduction, nil
	default:
		return "", fmt.Errorf("bootstrap: unknown substrate class %q", class)
	}
}

func EdgeUserNameFor(class string) (string, error) {
	names, err := edgeNamesFor(class)
	if err != nil {
		return "", err
	}
	return names.user, nil
}

func ClassParamNames(class string) ([]string, error) {
	names, err := edgeNamesFor(class)
	if err != nil {
		return nil, err
	}
	params := append(names.edgeParams(), names.originSecretParam)
	if class == ClassPreview {
		params = append(params, PreviewDomainParamName)
	}
	return params, nil
}

func PassphraseHeldBySibling(ctx context.Context, api CFNDescriber, class string) (bool, error) {
	sibling, err := SiblingClassOf(class)
	if err != nil {
		return false, err
	}
	stackName, err := StackNameFor(sibling)
	if err != nil {
		return false, err
	}
	out, err := stackOutputs(ctx, api, stackName)
	if err != nil {
		return false, err
	}
	return out != nil, nil
}

func featureStackNames(names []string, class string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, FeatureStackName(name, class))
	}
	return out
}

func FeatureDeleteOrder(names []string) ([]string, error) {
	levels, err := featureLevels(names)
	if err != nil {
		return nil, err
	}
	var out []string
	for i := len(levels) - 1; i >= 0; i-- {
		out = append(out, levels[i]...)
	}
	return out, nil
}

func deleteFeatureStacks(ctx context.Context, cfn CFNTeardownAPI, class string, names []string, log func(string)) error {
	order, err := FeatureDeleteOrder(names)
	if err != nil {
		return err
	}
	for _, name := range order {
		stackName := FeatureStackName(name, class)
		out, err := stackOutputs(ctx, cfn, stackName)
		if err != nil {
			return err
		}
		if out == nil {
			continue
		}
		if err := deleteCFNStack(ctx, cfn, stackName); err != nil {
			return err
		}
		if log != nil {
			log(fmt.Sprintf("removed %s", stackName))
		}
	}
	return nil
}

func Teardown(ctx context.Context, apis TeardownAPIs, class string, progress, log func(string)) error {
	report := func(f func(string), msg string) {
		if f != nil {
			f(msg)
		}
	}

	stackName, err := StackNameFor(class)
	if err != nil {
		return err
	}
	userName, err := EdgeUserNameFor(class)
	if err != nil {
		return err
	}
	params, err := ClassParamNames(class)
	if err != nil {
		return err
	}

	deployed, _, err := readSubstrate(ctx, apis.CFN, class)
	if err != nil {
		return err
	}

	if deployed.Present {
		report(progress, fmt.Sprintf("Deleting the access key of edge reader %s", userName))
		if err := deleteAccessKeys(ctx, apis.IAM, userName); err != nil {
			return err
		}

		for _, bucket := range []string{deployed.StateBucket, deployed.ArtifactBucket, deployed.AssetBucket} {
			if bucket == "" {
				continue
			}
			report(progress, fmt.Sprintf("Emptying %s", bucket))
			if err := emptyBucket(ctx, apis.Buckets, bucket); err != nil {
				return err
			}
		}

		standing, err := standingFeatures(ctx, apis.CFN, class)
		if err != nil {
			return err
		}
		present := standing.Names()
		if len(present) > 0 {
			report(progress, fmt.Sprintf("Deleting %s (CloudFormation)", strings.Join(featureStackNames(present, class), ", ")))
			if err := deleteFeatureStacks(ctx, apis.CFN, class, present, func(msg string) { report(log, msg) }); err != nil {
				return err
			}
		}

		report(progress, fmt.Sprintf("Deleting %s (CloudFormation)", stackName))
		if err := deleteCFNStack(ctx, apis.CFN, stackName); err != nil {
			return err
		}
	} else {
		report(log, fmt.Sprintf("no %s stack in this account; only the parameters it left behind are removed", stackName))
	}

	report(progress, "Deleting the substrate's stored parameters (SSM)")
	shared, err := PassphraseHeldBySibling(ctx, apis.CFN, class)
	if err != nil {
		return err
	}
	if !shared {
		params = append(params, PassphraseParamName)
	} else {
		report(log, fmt.Sprintf("the %s substrate is still bootstrapped and its Pulumi state is encrypted under the shared passphrase in %s; it stays", siblingName(class), PassphraseParamName))
	}
	for _, name := range params {
		if err := deleteParam(ctx, apis.SSM, name); err != nil {
			return err
		}
	}
	return nil
}

func siblingName(class string) string {
	sibling, err := SiblingClassOf(class)
	if err != nil {
		return ""
	}
	return sibling
}

func deleteParam(ctx context.Context, ssmClient SSMAPI, name string) error {
	if _, err := ssmClient.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(name)}); err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return nil
		}
		return fmt.Errorf("delete parameter %s: %w", name, err)
	}
	return nil
}

func deleteAccessKeys(ctx context.Context, iamClient IAMKeyAPI, userName string) error {
	out, err := iamClient.ListAccessKeys(ctx, &iam.ListAccessKeysInput{UserName: aws.String(userName)})
	if err != nil {
		var noUser *iamtypes.NoSuchEntityException
		if errors.As(err, &noUser) {
			return nil
		}
		return fmt.Errorf("list access keys for %s: %w", userName, err)
	}
	for _, key := range out.AccessKeyMetadata {
		if _, err := iamClient.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{
			UserName:    aws.String(userName),
			AccessKeyId: key.AccessKeyId,
		}); err != nil {
			var noKey *iamtypes.NoSuchEntityException
			if errors.As(err, &noKey) {
				continue
			}
			return fmt.Errorf("delete access key %s of %s: %w", aws.ToString(key.AccessKeyId), userName, err)
		}
	}
	return nil
}

const deleteBatchSize = 1000

func emptyBucket(ctx context.Context, api BucketEmptierAPI, bucket string) error {
	var keyMarker, versionMarker *string
	for {
		out, err := api.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
			Bucket:          aws.String(bucket),
			KeyMarker:       keyMarker,
			VersionIdMarker: versionMarker,
			MaxKeys:         aws.Int32(deleteBatchSize),
		})
		if err != nil {
			if bucketGone(err) {
				return nil
			}
			return fmt.Errorf("list %s: %w", bucket, err)
		}
		ids := make([]s3types.ObjectIdentifier, 0, len(out.Versions)+len(out.DeleteMarkers))
		for _, v := range out.Versions {
			ids = append(ids, s3types.ObjectIdentifier{Key: v.Key, VersionId: v.VersionId})
		}
		for _, m := range out.DeleteMarkers {
			ids = append(ids, s3types.ObjectIdentifier{Key: m.Key, VersionId: m.VersionId})
		}
		if len(ids) > 0 {
			deleted, err := api.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(bucket),
				Delete: &s3types.Delete{Objects: ids, Quiet: aws.Bool(true)},
			})
			if err != nil {
				if bucketGone(err) {
					return nil
				}
				return fmt.Errorf("delete objects in %s: %w", bucket, err)
			}
			if err := refusedObjects(bucket, deleted.Errors); err != nil {
				return err
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			return nil
		}
		keyMarker, versionMarker = out.NextKeyMarker, out.NextVersionIdMarker
	}
}

func bucketGone(err error) bool {
	var missing *s3types.NoSuchBucket
	if errors.As(err, &missing) {
		return true
	}
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchBucket" || apiErr.ErrorCode() == "NotFound")
}

const refusedObjectsShown = 10

func refusedObjects(bucket string, refused []s3types.Error) error {
	if len(refused) == 0 {
		return nil
	}
	shown := refused
	if len(shown) > refusedObjectsShown {
		shown = shown[:refusedObjectsShown]
	}
	reasons := make([]string, 0, len(shown))
	for _, e := range shown {
		reasons = append(reasons, fmt.Sprintf("%s: %s (%s)", aws.ToString(e.Key), aws.ToString(e.Message), aws.ToString(e.Code)))
	}
	more := ""
	if len(refused) > len(shown) {
		more = fmt.Sprintf(" (and %d more)", len(refused)-len(shown))
	}
	return fmt.Errorf("empty %s: S3 refused %d object(s): %s%s", bucket, len(refused), strings.Join(reasons, "; "), more)
}

func deleteCFNStack(ctx context.Context, cfn CFNTeardownAPI, stackName string) error {
	if _, err := cfn.DeleteStack(ctx, &cloudformation.DeleteStackInput{StackName: aws.String(stackName)}); err != nil {
		return fmt.Errorf("delete %s stack: %w", stackName, err)
	}
	w := cloudformation.NewStackDeleteCompleteWaiter(cfn)
	if err := w.Wait(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stackName)}, stackWaitTimeout); err != nil {
		return fmt.Errorf("wait for %s delete: %w", stackName, err)
	}
	return nil
}
