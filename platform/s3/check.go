package s3

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

type Want struct {
	Public  bool
	Origins []string
}

type inspectAPI interface {
	HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	GetBucketCors(context.Context, *s3.GetBucketCorsInput, ...func(*s3.Options)) (*s3.GetBucketCorsOutput, error)
	GetBucketPolicy(context.Context, *s3.GetBucketPolicyInput, ...func(*s3.Options)) (*s3.GetBucketPolicyOutput, error)
}

func Check(ctx context.Context, record *bindingsv1.BucketProperties, want Want) ([]string, error) {
	return check(ctx, storeOf(record).Client(), record, want)
}

func check(ctx context.Context, api inspectAPI, record *bindingsv1.BucketProperties, want Want) ([]string, error) {
	bucket := aws.String(record.GetBucket())
	if _, err := api.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: bucket}); err != nil {
		return nil, fmt.Errorf("bucket %s did not answer: %s", record.GetBucket(), scrubbed(err, record))
	}
	var warnings []string
	if len(want.Origins) > 0 {
		warning, err := checkOrigins(ctx, api, record, want.Origins)
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, warning...)
	}
	if want.Public {
		warning, err := checkPublic(ctx, api, record)
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, warning...)
	}
	return warnings, nil
}

func checkOrigins(ctx context.Context, api inspectAPI, record *bindingsv1.BucketProperties, origins []string) ([]string, error) {
	cors, err := api.GetBucketCors(ctx, &s3.GetBucketCorsInput{Bucket: aws.String(record.GetBucket())})
	missing := slices.Clone(origins)
	switch {
	case code(err) == "NoSuchCORSConfiguration":
	case err != nil:
		return []string{fmt.Sprintf("could not read bucket %s's CORS rules (%s), so ocel cannot check that they allow %s: make sure they do",
			record.GetBucket(), scrubbed(err, record), strings.Join(origins, ", "))}, nil
	default:
		missing = slices.DeleteFunc(missing, func(origin string) bool {
			return slices.ContainsFunc(cors.CORSRules, func(rule s3types.CORSRule) bool { return allows(rule.AllowedOrigins, origin) })
		})
	}
	if len(missing) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("the code allows uploads to bucket %s from %s, and its CORS rules allow no request from %s. Ocel has no credential to change them: add %s to the bucket's CORS rules",
		record.GetBucket(), strings.Join(origins, ", "), strings.Join(missing, ", "), strings.Join(missing, ", "))
}

func allows(allowed []string, origin string) bool {
	for _, pattern := range allowed {
		if pattern == "*" || pattern == origin {
			return true
		}
		if head, tail, wild := strings.Cut(pattern, "*"); wild && strings.HasPrefix(origin, head) && strings.HasSuffix(origin, tail) && len(origin) >= len(head)+len(tail) {
			return true
		}
	}
	return false
}

func checkPublic(ctx context.Context, api inspectAPI, record *bindingsv1.BucketProperties) ([]string, error) {
	policy, err := api.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{Bucket: aws.String(record.GetBucket())})
	refusal := fmt.Errorf("the code declares bucket %s public, and its policy lets nobody read %s anonymously. Ocel has no credential to change it: grant s3:GetObject to Principal \"*\" on arn:aws:s3:::%s/%s*, or make it public the way the store offers",
		record.GetBucket(), objectsOf(record), record.GetBucket(), prefixOf(record))
	switch {
	case code(err) == "NoSuchBucketPolicy":
		return nil, refusal
	case err != nil:
		return []string{fmt.Sprintf("could not read bucket %s's policy (%s), so ocel cannot check that anyone may read its objects: make sure they can",
			record.GetBucket(), scrubbed(err, record))}, nil
	}
	if !publicRead(aws.ToString(policy.Policy), record) {
		return nil, refusal
	}
	return nil, nil
}

func objectsOf(record *bindingsv1.BucketProperties) string {
	if prefix := prefixOf(record); prefix != "" {
		return "the objects under " + prefix
	}
	return "its objects"
}

func prefixOf(record *bindingsv1.BucketProperties) string {
	prefix := strings.Trim(record.GetPrefix(), "/")
	if prefix == "" {
		return ""
	}
	return prefix + "/"
}

type policyDocument struct {
	Statement []policyStatement `json:"Statement"`
}

type policyStatement struct {
	Effect    string          `json:"Effect"`
	Principal json.RawMessage `json:"Principal"`
	Action    stringOrList    `json:"Action"`
	Resource  stringOrList    `json:"Resource"`
	Condition json.RawMessage `json:"Condition"`
}

type stringOrList []string

func (s *stringOrList) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*s = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

func publicRead(policy string, record *bindingsv1.BucketProperties) bool {
	var document policyDocument
	if err := json.Unmarshal([]byte(policy), &document); err != nil {
		return false
	}
	objects := "arn:aws:s3:::" + record.GetBucket() + "/" + prefixOf(record)
	return slices.ContainsFunc(document.Statement, func(statement policyStatement) bool {
		return statement.Effect == "Allow" && len(statement.Condition) == 0 && anyone(statement.Principal) &&
			slices.ContainsFunc(statement.Action, func(action string) bool {
				return action == "*" || action == "s3:*" || action == "s3:GetObject" || action == "s3:Get*"
			}) &&
			slices.ContainsFunc(statement.Resource, func(resource string) bool { return covers(resource, objects) })
	})
}

func anyone(principal json.RawMessage) bool {
	var spelled string
	if err := json.Unmarshal(principal, &spelled); err == nil {
		return spelled == "*"
	}
	var keyed map[string]stringOrList
	if err := json.Unmarshal(principal, &keyed); err != nil {
		return false
	}
	return slices.Contains(keyed["AWS"], "*")
}

func covers(resource, objects string) bool {
	head, wild := strings.CutSuffix(resource, "*")
	return wild && strings.HasPrefix(objects, head)
}

func code(err error) string {
	var api smithy.APIError
	if errors.As(err, &api) {
		return api.ErrorCode()
	}
	return ""
}

func scrubbed(err error, record *bindingsv1.BucketProperties) string {
	message := err.Error()
	if secret := record.GetSecretAccessKey(); secret != "" {
		message = strings.ReplaceAll(message, secret, "[redacted]")
	}
	return message
}
