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

	"github.com/ocelhq/ocel/pkg/providerkit"
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

func varsKeyAPIs(crypto *fakeKMS) ParamAPIs {
	return ParamAPIs{KMS: crypto, Region: "eu-west-1"}
}

func TestVarsKeyBrought(t *testing.T) {
	t.Run("a stack owning no key still records the ARN", func(t *testing.T) {
		stack := varsKeyFeature.template(featureInputs{class: ClassProduction, varsKey: broughtKeyARN})
		tmpl := parseVarsTemplate(t, stack.body)

		for _, name := range []string{"VarsKey", "VarsKeyAlias"} {
			if _, ok := tmpl.Resources[name]; ok {
				t.Errorf("the stack declares %s over a key it was handed; ocel owns nothing it did not make", name)
			}
		}
		out, ok := tmpl.Outputs[outputVarsKeyARN]
		if !ok {
			t.Fatalf("the stack does not output %s, so nothing records the key values are sealed under", outputVarsKeyARN)
		}
		if out.Value != broughtKeyARN {
			t.Errorf("%s = %q, want the ARN the project brought", outputVarsKeyARN, out.Value)
		}
	})
}

func TestVarsKeyValidation(t *testing.T) {
	ctx := context.Background()

	t.Run("a key that encrypts and decrypts is admitted", func(t *testing.T) {
		crypto := workingKey()
		if _, err := validateBroughtKey(ctx, varsKeyAPIs(crypto), ClassProduction, Request{VarsKey: broughtKeyARN}); err != nil {
			t.Fatalf("validateBroughtKey = %v, want a working key admitted", err)
		}
		if len(crypto.context) == 0 {
			t.Error("the probe encrypted under no encryption context")
		}
	})

	t.Run("a bootstrap that brings no key is checked against nothing", func(t *testing.T) {
		crypto := &fakeKMS{describe: errors.New("DescribeKey should not be reached")}
		if _, err := validateBroughtKey(ctx, varsKeyAPIs(crypto), ClassProduction, Request{}); err != nil {
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
			_, err := validateBroughtKey(ctx, varsKeyAPIs(tc.crypto()), ClassProduction, Request{VarsKey: broughtKeyARN})
			if err == nil {
				t.Fatal("validateBroughtKey = nil, want a refusal")
			}
			var refusal providerkit.Refusal
			if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
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
