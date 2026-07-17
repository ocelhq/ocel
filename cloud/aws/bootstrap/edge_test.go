package bootstrap

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// fakeSSM keeps parameters in a map, returning a typed ParameterNotFound for a
// missing name so the errors.As path in ensureEdgeCredentials is exercised. It
// honours Overwrite: false the way SSM does.
type fakeSSM struct {
	params map[string]string
	puts   int
}

func newFakeSSM() *fakeSSM { return &fakeSSM{params: map[string]string{}} }

func (f *fakeSSM) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	v, ok := f.params[aws.ToString(in.Name)]
	if !ok {
		return nil, &ssmtypes.ParameterNotFound{}
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: aws.String(v)}}, nil
}

func (f *fakeSSM) PutParameter(_ context.Context, in *ssm.PutParameterInput, _ ...func(*ssm.Options)) (*ssm.PutParameterOutput, error) {
	f.puts++
	if _, exists := f.params[aws.ToString(in.Name)]; exists && !aws.ToBool(in.Overwrite) {
		return nil, &ssmtypes.ParameterAlreadyExists{}
	}
	f.params[aws.ToString(in.Name)] = aws.ToString(in.Value)
	return &ssm.PutParameterOutput{}, nil
}

// fakeIAM records the users it minted a key for and hands back a deterministic
// key so the stored payload can be asserted. existingKeys is the number of keys
// ListAccessKeys reports before any mint, so tests can drive the 2-key guard.
type fakeIAM struct {
	created      []string
	existingKeys int
}

func (f *fakeIAM) ListAccessKeys(_ context.Context, in *iam.ListAccessKeysInput, _ ...func(*iam.Options)) (*iam.ListAccessKeysOutput, error) {
	meta := make([]iamtypes.AccessKeyMetadata, f.existingKeys)
	for i := range meta {
		meta[i] = iamtypes.AccessKeyMetadata{UserName: in.UserName}
	}
	return &iam.ListAccessKeysOutput{AccessKeyMetadata: meta}, nil
}

func (f *fakeIAM) CreateAccessKey(_ context.Context, in *iam.CreateAccessKeyInput, _ ...func(*iam.Options)) (*iam.CreateAccessKeyOutput, error) {
	f.created = append(f.created, aws.ToString(in.UserName))
	return &iam.CreateAccessKeyOutput{AccessKey: &iamtypes.AccessKey{
		AccessKeyId:     aws.String("AKIAEDGE"),
		SecretAccessKey: aws.String("secret-edge"),
	}}, nil
}

func TestEnsureEdgeCredentials_MintsWhenAbsent(t *testing.T) {
	ssmc := newFakeSSM()
	iamc := &fakeIAM{}

	created, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, ClassProduction)
	if err != nil {
		t.Fatalf("ensureEdgeCredentials: %v", err)
	}
	if !created {
		t.Error("expected created=true on first mint")
	}
	if len(iamc.created) != 1 || iamc.created[0] != EdgeUserName {
		t.Errorf("CreateAccessKey users = %v, want [%s]", iamc.created, EdgeUserName)
	}

	stored, ok := ssmc.params[EdgeCredentialsParamName]
	if !ok {
		t.Fatalf("credentials were not written to %s", EdgeCredentialsParamName)
	}
	var creds EdgeCredentials
	if err := json.Unmarshal([]byte(stored), &creds); err != nil {
		t.Fatalf("stored value is not EdgeCredentials JSON: %v", err)
	}
	if creds.AccessKeyID != "AKIAEDGE" || creds.SecretAccessKey != "secret-edge" {
		t.Errorf("stored creds = %+v, want the minted key", creds)
	}
}

func TestEnsureEdgeCredentials_ReusesWhenPresent(t *testing.T) {
	ssmc := newFakeSSM()
	ssmc.params[EdgeCredentialsParamName] = `{"accessKeyId":"AKOLD","secretAccessKey":"old"}`
	iamc := &fakeIAM{}

	created, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, ClassProduction)
	if err != nil {
		t.Fatalf("ensureEdgeCredentials: %v", err)
	}
	if created {
		t.Error("expected created=false when the parameter already exists")
	}
	if len(iamc.created) != 0 {
		t.Errorf("minted a key despite an existing parameter: %v", iamc.created)
	}
	if ssmc.puts != 0 {
		t.Errorf("overwrote the existing parameter (%d puts)", ssmc.puts)
	}
}

func TestEnsureEdgeCredentials_FailsWhenKeyCapReached(t *testing.T) {
	ssmc := newFakeSSM()
	iamc := &fakeIAM{existingKeys: 2}

	_, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, ClassProduction)
	if err == nil {
		t.Fatal("expected an error when the user is already at the 2-key cap")
	}
	if len(iamc.created) != 0 {
		t.Errorf("minted a key despite the cap: %v", iamc.created)
	}
	if ssmc.puts != 0 {
		t.Errorf("wrote a parameter despite the cap (%d puts)", ssmc.puts)
	}
}

func TestEnsureEdgeCredentials_PreviewUsesPreviewIdentity(t *testing.T) {
	ssmc := newFakeSSM()
	iamc := &fakeIAM{}

	if _, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, ClassPreview); err != nil {
		t.Fatalf("ensureEdgeCredentials: %v", err)
	}
	if len(iamc.created) != 1 || iamc.created[0] != EdgePreviewUserName {
		t.Errorf("CreateAccessKey users = %v, want [%s]", iamc.created, EdgePreviewUserName)
	}
	if _, ok := ssmc.params[EdgeCredentialsPreviewParamName]; !ok {
		t.Errorf("preview credentials were not written to %s", EdgeCredentialsPreviewParamName)
	}
}

func TestReadEdgeCredentials(t *testing.T) {
	ssmc := newFakeSSM()
	ssmc.params[EdgeCredentialsParamName] = `{"accessKeyId":"AK1","secretAccessKey":"s1"}`

	creds, err := ReadEdgeCredentials(context.Background(), ssmc, ClassProduction)
	if err != nil {
		t.Fatalf("ReadEdgeCredentials: %v", err)
	}
	if creds.AccessKeyID != "AK1" || creds.SecretAccessKey != "s1" {
		t.Errorf("creds = %+v, want AK1/s1", creds)
	}
}

func TestEdgeCredentials_UnknownClass(t *testing.T) {
	if _, err := ensureEdgeCredentials(context.Background(), &fakeIAM{}, newFakeSSM(), "nonsense"); err == nil {
		t.Error("expected an error for an unknown substrate class")
	}
}
