package s3

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

type inspected struct {
	headErr   error
	cors      []s3types.CORSRule
	corsErr   error
	policy    string
	policyErr error
}

func (i inspected) HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return &s3.HeadBucketOutput{}, i.headErr
}

func (i inspected) GetBucketCors(context.Context, *s3.GetBucketCorsInput, ...func(*s3.Options)) (*s3.GetBucketCorsOutput, error) {
	if i.corsErr != nil {
		return nil, i.corsErr
	}
	return &s3.GetBucketCorsOutput{CORSRules: i.cors}, nil
}

func (i inspected) GetBucketPolicy(context.Context, *s3.GetBucketPolicyInput, ...func(*s3.Options)) (*s3.GetBucketPolicyOutput, error) {
	if i.policyErr != nil {
		return nil, i.policyErr
	}
	return &s3.GetBucketPolicyOutput{Policy: aws.String(i.policy)}, nil
}

func checked(api inspected, want Want) ([]string, error) {
	return check(context.Background(), api, &bindingsv1.BucketProperties{Bucket: "acme", Prefix: "uploads/", SecretAccessKey: "s3cr3t"}, want)
}

const publicPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::acme/*"}]}`

func TestCheck(t *testing.T) {
	t.Run("a bucket that answers and is asked for nothing more passes", func(t *testing.T) {
		warnings, err := checked(inspected{}, Want{})
		if err != nil || len(warnings) != 0 {
			t.Fatalf("check = %v, %v", warnings, err)
		}
	})

	t.Run("a bucket that does not answer is refused, the secret kept out", func(t *testing.T) {
		_, err := checked(inspected{headErr: &apiError{code: "NotFound: s3cr3t"}}, Want{})
		if err == nil {
			t.Fatal("check = nil, want an unreachable bucket refused")
		}
		if strings.Contains(err.Error(), "s3cr3t") {
			t.Errorf("check = %v, repeats the secret", err)
		}
	})

	t.Run("an origin the bucket's cors never allows is refused, naming it", func(t *testing.T) {
		_, err := checked(inspected{cors: []s3types.CORSRule{{AllowedOrigins: []string{"https://acme.com"}, AllowedMethods: []string{"PUT"}}}},
			Want{Origins: []string{"https://acme.com", "https://admin.acme.com"}})
		if err == nil || !strings.Contains(err.Error(), "https://admin.acme.com") || strings.Contains(err.Error(), `"https://acme.com"`) {
			t.Fatalf("check = %v, want the one missing origin named", err)
		}
	})

	t.Run("a wildcard origin covers every origin", func(t *testing.T) {
		if _, err := checked(inspected{cors: []s3types.CORSRule{{AllowedOrigins: []string{"*"}}}}, Want{Origins: []string{"https://acme.com"}}); err != nil {
			t.Fatalf("check = %v", err)
		}
	})

	t.Run("a bucket with no cors at all is refused when origins are declared", func(t *testing.T) {
		_, err := checked(inspected{corsErr: &apiError{code: "NoSuchCORSConfiguration"}}, Want{Origins: []string{"https://acme.com"}})
		if err == nil || !strings.Contains(err.Error(), "https://acme.com") {
			t.Fatalf("check = %v, want the origins it lacks named", err)
		}
	})

	t.Run("cors the credential may not read is a warning, not a refusal", func(t *testing.T) {
		warnings, err := checked(inspected{corsErr: &apiError{code: "AccessDenied"}}, Want{Origins: []string{"https://acme.com"}})
		if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "https://acme.com") {
			t.Fatalf("check = %v, %v, want one warning naming what could not be checked", warnings, err)
		}
	})

	t.Run("a public bucket needs a policy that lets anyone read its objects", func(t *testing.T) {
		if _, err := checked(inspected{policy: publicPolicy}, Want{Public: true}); err != nil {
			t.Fatalf("check = %v, want a public-read policy accepted", err)
		}
		_, err := checked(inspected{policy: `{"Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::1:root"},"Action":"s3:GetObject","Resource":"arn:aws:s3:::acme/*"}]}`}, Want{Public: true})
		if err == nil || !strings.Contains(err.Error(), "public") {
			t.Fatalf("check = %v, want a policy granting no anonymous read refused", err)
		}
		_, err = checked(inspected{policyErr: &apiError{code: "NoSuchBucketPolicy"}}, Want{Public: true})
		if err == nil {
			t.Fatal("check = nil, want a public bucket with no policy refused")
		}
	})

	t.Run("a policy covering only another prefix is refused", func(t *testing.T) {
		_, err := checked(inspected{policy: `{"Statement":[{"Effect":"Allow","Principal":"*","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::acme/avatars/*"]}]}`}, Want{Public: true})
		if err == nil {
			t.Fatal("check = nil, want a policy over another prefix refused")
		}
	})

	t.Run("a policy the store cannot answer for is a warning", func(t *testing.T) {
		warnings, err := checked(inspected{policyErr: &apiError{code: "NotImplemented"}}, Want{Public: true})
		if err != nil || len(warnings) != 1 {
			t.Fatalf("check = %v, %v, want one warning", warnings, err)
		}
	})

	t.Run("a transport failure reading cors is a warning", func(t *testing.T) {
		warnings, err := checked(inspected{corsErr: errors.New("connection reset")}, Want{Origins: []string{"https://acme.com"}})
		if err != nil || len(warnings) != 1 {
			t.Fatalf("check = %v, %v", warnings, err)
		}
	})
}
