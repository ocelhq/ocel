package bootstrap

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/ocelhq/ocel/cloud/edge"
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

func (f *fakeSSM) DeleteParameter(_ context.Context, in *ssm.DeleteParameterInput, _ ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error) {
	name := aws.ToString(in.Name)
	if _, exists := f.params[name]; !exists {
		return nil, &ssmtypes.ParameterNotFound{}
	}
	delete(f.params, name)
	return &ssm.DeleteParameterOutput{}, nil
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

// offeredStore is a complete cache-store offer as a freshly minted one arrives:
// every coordinate plus the secret. Tests drop the secret to model a reuse.
func offeredStore() map[string]string {
	return map[string]string{
		edge.OfferKeyBucket:          "ocel-edge-cache",
		edge.OfferKeyEndpoint:        "https://acct.r2.cloudflarestorage.com",
		edge.OfferKeyRegion:          "auto",
		edge.OfferKeyAccessKeyID:     "tok-1",
		edge.OfferKeySecretAccessKey: "sha-of-tok-1",
	}
}

func TestAdoptCacheStore_FreshMintPersistsEveryCoordinate(t *testing.T) {
	ssmc := newFakeSSM()

	if err := adoptCacheStore(context.Background(), ssmc, ClassProduction, "fake", offeredStore()); err != nil {
		t.Fatalf("adoptCacheStore: %v", err)
	}
	got, err := ReadCacheStore(context.Background(), ssmc, ClassProduction)
	if err != nil {
		t.Fatalf("ReadCacheStore: %v", err)
	}
	want := CacheStore{
		Bucket:          "ocel-edge-cache",
		Endpoint:        "https://acct.r2.cloudflarestorage.com",
		Region:          "auto",
		AccessKeyID:     "tok-1",
		SecretAccessKey: "sha-of-tok-1",
	}
	if got != want {
		t.Errorf("stored store = %+v, want %+v", got, want)
	}
}

// TestAdoptCacheStore_ReuseKeepsStoredSecret is the normal re-run: the edge
// reoffers the token it already minted, carrying no secret, and the secret
// already in SSM must survive.
func TestAdoptCacheStore_ReuseKeepsStoredSecret(t *testing.T) {
	ssmc := newFakeSSM()
	if err := adoptCacheStore(context.Background(), ssmc, ClassProduction, "fake", offeredStore()); err != nil {
		t.Fatalf("first adopt: %v", err)
	}

	reoffer := offeredStore()
	delete(reoffer, edge.OfferKeySecretAccessKey)
	reoffer[edge.OfferKeyEndpoint] = "https://acct.r2.cloudflarestorage.com/v2"

	if err := adoptCacheStore(context.Background(), ssmc, ClassProduction, "fake", reoffer); err != nil {
		t.Fatalf("second adopt: %v", err)
	}
	got, err := ReadCacheStore(context.Background(), ssmc, ClassProduction)
	if err != nil {
		t.Fatalf("ReadCacheStore: %v", err)
	}
	if got.SecretAccessKey != "sha-of-tok-1" {
		t.Errorf("secret = %q, want the stored secret preserved", got.SecretAccessKey)
	}
	if got.Endpoint != "https://acct.r2.cloudflarestorage.com/v2" {
		t.Errorf("endpoint = %q, want the reoffered coordinate", got.Endpoint)
	}
}

// TestAdoptCacheStore_DanglingToken covers both shapes of the cross-run hazard: a
// secretless offer whose access key is not the one in SSM means a prior run
// minted a token and never persisted it. The secret is gone for good, so bootstrap
// must say which token to delete rather than store a credential-less config.
func TestAdoptCacheStore_DanglingToken(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored string
	}{
		{"nothing stored", ""},
		{"a different key stored", `{"bucket":"ocel-edge-cache","accessKeyId":"tok-0","secretAccessKey":"sha-of-tok-0"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ssmc := newFakeSSM()
			if tc.stored != "" {
				ssmc.params[CacheStoreParamName] = tc.stored
			}
			offer := offeredStore()
			delete(offer, edge.OfferKeySecretAccessKey)

			err := adoptCacheStore(context.Background(), ssmc, ClassProduction, "fake", offer)
			if err == nil {
				t.Fatal("expected a dangling-token error for a secretless offer with no matching stored secret")
			}
			if !strings.Contains(err.Error(), "tok-1") {
				t.Errorf("diagnostic does not name the token: %v", err)
			}
			if ssmc.params[CacheStoreParamName] != tc.stored {
				t.Errorf("wrote %q over the stored store despite failing", ssmc.params[CacheStoreParamName])
			}
		})
	}
}

func TestAdoptCacheStore_PreviewStoresSeparately(t *testing.T) {
	ssmc := newFakeSSM()
	preview := offeredStore()
	preview[edge.OfferKeyBucket] = "ocel-edge-cache-preview"
	preview[edge.OfferKeyAccessKeyID] = "tok-preview"

	if err := adoptCacheStore(context.Background(), ssmc, ClassProduction, "fake", offeredStore()); err != nil {
		t.Fatalf("production adopt: %v", err)
	}
	if err := adoptCacheStore(context.Background(), ssmc, ClassPreview, "fake", preview); err != nil {
		t.Fatalf("preview adopt: %v", err)
	}

	prod, err := ReadCacheStore(context.Background(), ssmc, ClassProduction)
	if err != nil {
		t.Fatalf("ReadCacheStore production: %v", err)
	}
	prev, err := ReadCacheStore(context.Background(), ssmc, ClassPreview)
	if err != nil {
		t.Fatalf("ReadCacheStore preview: %v", err)
	}
	if prod.Bucket != "ocel-edge-cache" || prod.AccessKeyID != "tok-1" {
		t.Errorf("production store = %+v, want production's own coordinates", prod)
	}
	if prev.Bucket != "ocel-edge-cache-preview" || prev.AccessKeyID != "tok-preview" {
		t.Errorf("preview store = %+v, want preview's own coordinates", prev)
	}
}

func TestReadCacheStore_AbsentIsNotAnError(t *testing.T) {
	got, err := ReadCacheStore(context.Background(), newFakeSSM(), ClassProduction)
	if err != nil {
		t.Fatalf("ReadCacheStore on an absent parameter: %v", err)
	}
	if got != (CacheStore{}) {
		t.Errorf("ReadCacheStore = %+v, want the zero store", got)
	}
}

func offeredDeploymentsStore() map[string]string {
	return map[string]string{
		edge.OfferKeyStoreEndpoint:      "https://ocel-deployments-store.acct.workers.dev",
		edge.OfferKeyStoreScriptName:    "ocel-deployments-store",
		edge.OfferKeyStoreBootstrapCred: "cred-prod",
	}
}

func TestDeploymentsStoreParamFor(t *testing.T) {
	for _, tc := range []struct {
		class string
		want  string
	}{
		{ClassProduction, DeploymentsStoreParamName},
		{ClassPreview, DeploymentsStorePreviewParamName},
	} {
		got, err := DeploymentsStoreParamFor(tc.class)
		if err != nil {
			t.Fatalf("DeploymentsStoreParamFor(%q): %v", tc.class, err)
		}
		if got != tc.want {
			t.Errorf("DeploymentsStoreParamFor(%q) = %q, want %q", tc.class, got, tc.want)
		}
	}
	if _, err := DeploymentsStoreParamFor("nonsense"); err == nil {
		t.Error("DeploymentsStoreParamFor(unknown class) = nil error, want an error")
	}
	if DeploymentsStoreParamName == DeploymentsStorePreviewParamName {
		t.Error("production and preview deployments-store parameters must differ")
	}
}

func TestAdoptDeploymentsStore_PreviewStoresSeparately(t *testing.T) {
	ssmc := newFakeSSM()
	preview := offeredDeploymentsStore()
	preview[edge.OfferKeyStoreEndpoint] = "https://ocel-deployments-store-preview.acct.workers.dev"
	preview[edge.OfferKeyStoreScriptName] = "ocel-deployments-store-preview"
	preview[edge.OfferKeyStoreBootstrapCred] = "cred-preview"

	if err := adoptDeploymentsStore(context.Background(), ssmc, ClassProduction, offeredDeploymentsStore()); err != nil {
		t.Fatalf("production adopt: %v", err)
	}
	if err := adoptDeploymentsStore(context.Background(), ssmc, ClassPreview, preview); err != nil {
		t.Fatalf("preview adopt: %v", err)
	}

	prod, err := ReadDeploymentsStore(context.Background(), ssmc)
	if err != nil {
		t.Fatalf("ReadDeploymentsStore: %v", err)
	}
	prev, err := ReadDeploymentsStorePreview(context.Background(), ssmc)
	if err != nil {
		t.Fatalf("ReadDeploymentsStorePreview: %v", err)
	}
	wantProd := DeploymentsStore{Endpoint: "https://ocel-deployments-store.acct.workers.dev", ScriptName: "ocel-deployments-store", BootstrapCred: "cred-prod"}
	wantPrev := DeploymentsStore{Endpoint: "https://ocel-deployments-store-preview.acct.workers.dev", ScriptName: "ocel-deployments-store-preview", BootstrapCred: "cred-preview"}
	if prod != wantProd {
		t.Errorf("production store = %+v, want %+v", prod, wantProd)
	}
	if prev != wantPrev {
		t.Errorf("preview store = %+v, want %+v", prev, wantPrev)
	}
}

func TestReadDeploymentsStore_AbsentIsNotAnError(t *testing.T) {
	for _, class := range []string{ClassProduction, ClassPreview} {
		got, err := ReadDeploymentsStoreFor(context.Background(), newFakeSSM(), class)
		if err != nil {
			t.Fatalf("ReadDeploymentsStoreFor(%q) on an absent parameter: %v", class, err)
		}
		if got != (DeploymentsStore{}) {
			t.Errorf("ReadDeploymentsStoreFor(%q) = %+v, want the zero store", class, got)
		}
	}
}

func TestAdoptCacheStore_UnknownClass(t *testing.T) {
	if err := adoptCacheStore(context.Background(), newFakeSSM(), "nonsense", "fake", offeredStore()); err == nil {
		t.Error("expected an error for an unknown substrate class")
	}
}
