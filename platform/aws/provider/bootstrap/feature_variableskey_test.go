package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const broughtKeyARN = "arn:aws:kms:eu-west-1:123456789012:key/brought-1234"

type fakeKMS struct {
	metadata *kmstypes.KeyMetadata
	describe error
	encrypt  error
	decrypt  error

	sealed  []byte
	context map[string]string
}

func workingKey() *fakeKMS {
	return &fakeKMS{metadata: &kmstypes.KeyMetadata{
		Arn:      aws.String(broughtKeyARN),
		Enabled:  true,
		KeySpec:  kmstypes.KeySpecSymmetricDefault,
		KeyUsage: kmstypes.KeyUsageTypeEncryptDecrypt,
	}}
}

func (f *fakeKMS) DescribeKey(_ context.Context, _ *kms.DescribeKeyInput, _ ...func(*kms.Options)) (*kms.DescribeKeyOutput, error) {
	if f.describe != nil {
		return nil, f.describe
	}
	return &kms.DescribeKeyOutput{KeyMetadata: f.metadata}, nil
}

func (f *fakeKMS) Encrypt(_ context.Context, in *kms.EncryptInput, _ ...func(*kms.Options)) (*kms.EncryptOutput, error) {
	if f.encrypt != nil {
		return nil, f.encrypt
	}
	f.context = in.EncryptionContext
	f.sealed = append([]byte("sealed:"), in.Plaintext...)
	return &kms.EncryptOutput{CiphertextBlob: f.sealed}, nil
}

func (f *fakeKMS) Decrypt(_ context.Context, in *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	if f.decrypt != nil {
		return nil, f.decrypt
	}
	return &kms.DecryptOutput{Plaintext: bytes.TrimPrefix(in.CiphertextBlob, []byte("sealed:"))}, nil
}

func variablesKeyAPIs(crypto *fakeKMS) ParamAPIs {
	return ParamAPIs{KMS: crypto, Region: "eu-west-1"}
}

func TestABroughtVariablesKeyIsRecordedWithoutAStackOwningIt(t *testing.T) {
	t.Run("a stack owning no key still records the ARN", func(t *testing.T) {
		stack := variablesKeyFeature.template(featureInputs{ns: defaultNamespace, tier: environment.TierProduction, variablesKey: broughtKeyARN})
		tmpl := parseVariablesTemplate(t, stack.body)

		for name, resource := range tmpl.Resources {
			if name == "VariablesKey" || name == "VariablesKeyAlias" || strings.HasPrefix(resource.Type, "AWS::KMS::") {
				t.Errorf("the stack declares %s (%s) over a key it was handed; creating an alias on it needs kms:CreateAlias from the key's own policy, which ocel never writes", name, resource.Type)
			}
		}
		out, ok := tmpl.Outputs[outputVariablesKeyARN]
		if !ok {
			t.Fatalf("the stack does not output %s, so nothing records the key values are sealed under", outputVariablesKeyARN)
		}
		if out.Value != broughtKeyARN {
			t.Errorf("%s = %q, want the ARN the project brought", outputVariablesKeyARN, out.Value)
		}
	})
}

func TestABroughtVariablesKeyIsAdmittedOnlyIfItEncryptsAndDecrypts(t *testing.T) {
	ctx := context.Background()

	t.Run("a key that encrypts and decrypts is admitted", func(t *testing.T) {
		crypto := workingKey()
		if _, err := validateBroughtKey(ctx, variablesKeyAPIs(crypto), defaultNamespace, environment.TierProduction, Request{VariablesKey: broughtKeyARN}); err != nil {
			t.Fatalf("validateBroughtKey = %v, want a working key admitted", err)
		}
		if len(crypto.context) == 0 {
			t.Error("the probe encrypted under no encryption context")
		}
	})

	t.Run("a bootstrap that brings no key is checked against nothing", func(t *testing.T) {
		crypto := &fakeKMS{describe: errors.New("DescribeKey should not be reached")}
		if _, err := validateBroughtKey(ctx, variablesKeyAPIs(crypto), defaultNamespace, environment.TierProduction, Request{}); err != nil {
			t.Fatalf("validateBroughtKey = %v, want nothing checked where nothing was brought", err)
		}
	})

	for _, tc := range []struct {
		name   string
		crypto func() *fakeKMS
		says   string
	}{
		{
			name: "asymmetric",
			crypto: func() *fakeKMS {
				crypto := workingKey()
				crypto.metadata.KeySpec = kmstypes.KeySpecRsa4096
				return crypto
			},
			says: "symmetric",
		},
		{
			name: "signing only",
			crypto: func() *fakeKMS {
				crypto := workingKey()
				crypto.metadata.KeyUsage = kmstypes.KeyUsageTypeSignVerify
				return crypto
			},
			says: "symmetric",
		},
		{
			name: "disabled",
			crypto: func() *fakeKMS {
				crypto := workingKey()
				crypto.metadata.Enabled = false
				return crypto
			},
			says: "enabled",
		},
		{
			name: "another region",
			crypto: func() *fakeKMS {
				crypto := workingKey()
				crypto.metadata.Arn = aws.String("arn:aws:kms:us-east-1:123456789012:key/brought-1234")
				return crypto
			},
			says: "region",
		},
		{
			name: "malformed ARN",
			crypto: func() *fakeKMS {
				crypto := workingKey()
				crypto.metadata.Arn = aws.String("brought-1234")
				return crypto
			},
			says: "is not a key ARN",
		},
		{
			name: "unreadable",
			crypto: func() *fakeKMS {
				crypto := workingKey()
				crypto.describe = errors.New("AccessDeniedException")
				return crypto
			},
			says: "key policy",
		},
		{
			name: "undecryptable",
			crypto: func() *fakeKMS {
				crypto := workingKey()
				crypto.decrypt = errors.New("AccessDeniedException")
				return crypto
			},
			says: "key policy",
		},
	} {
		t.Run("a "+tc.name+" key is refused", func(t *testing.T) {
			_, err := validateBroughtKey(ctx, variablesKeyAPIs(tc.crypto()), defaultNamespace, environment.TierProduction, Request{VariablesKey: broughtKeyARN})
			if err == nil {
				t.Fatal("validateBroughtKey = nil, want a refusal")
			}
			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
				t.Fatalf("validateBroughtKey = %v, want a CodeInvalid refusal", err)
			}
			if !strings.Contains(err.Error(), broughtKeyARN) {
				t.Errorf("refusal = %v, want it to name the key brought", err)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("refusal = %v, want it to say %q", err, tc.says)
			}
		})
	}
}

func TestABroughtVariablesKeyRefusalNamesThePolicyThatDenied(t *testing.T) {
	ctx := context.Background()
	const denial = "operation error KMS: DescribeKey, https response error StatusCode: 400, api error AccessDeniedException: User: arn:aws:sts::123456789012:assumed-role/ocel-ci/session is not authorized to perform: kms:DescribeKey on resource: " + broughtKeyARN + " because "

	for _, tc := range []struct {
		name    string
		because string
		says    []string
		saysNot []string
	}{
		{
			name:    "no session policy allows it",
			because: "no session policy allows the kms:DescribeKey action",
			says:    []string{"ocel permissions bootstrap"},
			saysNot: []string{"key policy"},
		},
		{
			name:    "no identity-based policy allows it",
			because: "no identity-based policy allows the kms:DescribeKey action",
			says:    []string{"ocel permissions bootstrap"},
			saysNot: []string{"key policy"},
		},
		{
			name:    "no resource-based policy allows it",
			because: "no resource-based policy allows the kms:DescribeKey action",
			says:    []string{"key policy"},
			saysNot: []string{"ocel permissions bootstrap"},
		},
		{
			name:    "the denial names no policy",
			because: "access was denied",
			says:    []string{"key policy", "ocel permissions bootstrap"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			crypto := workingKey()
			crypto.describe = errors.New(denial + tc.because)
			_, err := validateBroughtKey(ctx, variablesKeyAPIs(crypto), defaultNamespace, environment.TierProduction, Request{VariablesKey: broughtKeyARN})
			if err == nil {
				t.Fatal("validateBroughtKey = nil, want a refusal")
			}
			for _, want := range tc.says {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal = %v, want it to say %q", err, want)
				}
			}
			for _, unwanted := range tc.saysNot {
				if strings.Contains(err.Error(), unwanted) {
					t.Errorf("refusal = %v, want it not to say %q", err, unwanted)
				}
			}
		})
	}
}
