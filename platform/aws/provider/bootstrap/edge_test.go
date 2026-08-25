package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type fakeSSM struct {
	params       map[string]string
	descriptions map[string]string
	puts         int
}

func newFakeSSM() *fakeSSM {
	return &fakeSSM{params: map[string]string{}, descriptions: map[string]string{}}
}

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
	f.descriptions[aws.ToString(in.Name)] = aws.ToString(in.Description)
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

type fakeIAM struct {
	created []string
	keys    []string
	deleted []string
}

func (f *fakeIAM) DeleteAccessKey(_ context.Context, in *iam.DeleteAccessKeyInput, _ ...func(*iam.Options)) (*iam.DeleteAccessKeyOutput, error) {
	id := aws.ToString(in.AccessKeyId)
	if at := slices.Index(f.keys, id); at >= 0 {
		f.keys = slices.Delete(f.keys, at, at+1)
	}
	f.deleted = append(f.deleted, id)
	return &iam.DeleteAccessKeyOutput{}, nil
}

func (f *fakeIAM) ListAccessKeys(_ context.Context, in *iam.ListAccessKeysInput, _ ...func(*iam.Options)) (*iam.ListAccessKeysOutput, error) {
	meta := make([]iamtypes.AccessKeyMetadata, 0, len(f.keys))
	for _, id := range f.keys {
		meta = append(meta, iamtypes.AccessKeyMetadata{UserName: in.UserName, AccessKeyId: aws.String(id)})
	}
	return &iam.ListAccessKeysOutput{AccessKeyMetadata: meta}, nil
}

func (f *fakeIAM) CreateAccessKey(_ context.Context, in *iam.CreateAccessKeyInput, _ ...func(*iam.Options)) (*iam.CreateAccessKeyOutput, error) {
	f.created = append(f.created, aws.ToString(in.UserName))
	id := "AKIAEDGE"
	if len(f.created) > 1 {
		id = fmt.Sprintf("AKIAEDGE%d", len(f.created))
	}
	f.keys = append(f.keys, id)
	return &iam.CreateAccessKeyOutput{AccessKey: &iamtypes.AccessKey{
		AccessKeyId:     aws.String(id),
		SecretAccessKey: aws.String("secret-edge"),
	}}, nil
}

func TestEnsureEdgeCredentials(t *testing.T) {
	t.Run("mints when absent", func(t *testing.T) {
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
	})

	t.Run("reuses a recorded key the user still has", func(t *testing.T) {
		ssmc := newFakeSSM()
		ssmc.params[EdgeCredentialsParamName] = `{"accessKeyId":"AKOLD","secretAccessKey":"old"}`
		iamc := &fakeIAM{keys: []string{"AKOLD"}}

		created, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, ClassProduction)
		if err != nil {
			t.Fatalf("ensureEdgeCredentials: %v", err)
		}
		if created {
			t.Error("expected created=false when the recorded key is still live")
		}
		if len(iamc.created) != 0 {
			t.Errorf("minted a key despite a live recorded one: %v", iamc.created)
		}
		if ssmc.puts != 0 {
			t.Errorf("overwrote the existing parameter (%d puts)", ssmc.puts)
		}
	})

	t.Run("re-mints when the recorded key is gone from the user", func(t *testing.T) {
		ssmc := newFakeSSM()
		ssmc.params[EdgeCredentialsParamName] = `{"accessKeyId":"AKGONE","secretAccessKey":"gone"}`
		iamc := &fakeIAM{}

		created, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, ClassProduction)
		if err != nil {
			t.Fatalf("ensureEdgeCredentials: %v", err)
		}
		if !created {
			t.Error("expected created=true: a CloudFormation replacement of the user takes its key, and the parameter outlives it")
		}
		var creds EdgeCredentials
		if err := json.Unmarshal([]byte(ssmc.params[EdgeCredentialsParamName]), &creds); err != nil {
			t.Fatalf("stored value is not EdgeCredentials JSON: %v", err)
		}
		if creds.AccessKeyID != "AKIAEDGE" {
			t.Errorf("stored key = %q, want the freshly minted AKIAEDGE: the edge signs with a key AWS no longer knows otherwise", creds.AccessKeyID)
		}
	})

	t.Run("names the dead key when the cap blocks a re-mint", func(t *testing.T) {
		ssmc := newFakeSSM()
		ssmc.params[EdgeCredentialsParamName] = `{"accessKeyId":"AKGONE","secretAccessKey":"gone"}`
		iamc := &fakeIAM{keys: []string{"AK1", "AK2"}}

		_, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, ClassProduction)
		if err == nil {
			t.Fatal("expected an error when the user is already at the 2-key cap")
		}
		if !strings.Contains(err.Error(), "AKGONE") {
			t.Errorf("err = %v, want it to name the key the parameter still claims", err)
		}
	})

	t.Run("fails when key cap reached", func(t *testing.T) {
		ssmc := newFakeSSM()
		iamc := &fakeIAM{keys: []string{"AK1", "AK2"}}

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
	})

	t.Run("preview uses preview identity", func(t *testing.T) {
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
	})
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

func TestEdgeCredentials(t *testing.T) {
	t.Run("unknown class", func(t *testing.T) {
		if _, err := ensureEdgeCredentials(context.Background(), &fakeIAM{}, newFakeSSM(), "nonsense"); err == nil {
			t.Error("expected an error for an unknown class")
		}
	})
}

func offeredStore() map[string]string {
	return map[string]string{
		edge.OfferKeyBucket:          "ocel-edge-cache",
		edge.OfferKeyEndpoint:        "https://acct.r2.cloudflarestorage.com",
		edge.OfferKeyRegion:          "auto",
		edge.OfferKeyAccessKeyID:     "tok-1",
		edge.OfferKeySecretAccessKey: "sha-of-tok-1",
	}
}

func TestAdoptCacheStore(t *testing.T) {
	t.Run("fresh mint persists every coordinate", func(t *testing.T) {
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
	})

	t.Run("reuse keeps stored secret", func(t *testing.T) {
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
	})

	t.Run("dangling token", func(t *testing.T) {
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
	})

	t.Run("preview stores separately", func(t *testing.T) {
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
	})

	t.Run("unknown class", func(t *testing.T) {
		if err := adoptCacheStore(context.Background(), newFakeSSM(), "nonsense", "fake", offeredStore()); err == nil {
			t.Error("expected an error for an unknown class")
		}
	})
}

func TestReadCacheStore(t *testing.T) {
	t.Run("absent is not an error", func(t *testing.T) {
		got, err := ReadCacheStore(context.Background(), newFakeSSM(), ClassProduction)
		if err != nil {
			t.Fatalf("ReadCacheStore on an absent parameter: %v", err)
		}
		if got != (CacheStore{}) {
			t.Errorf("ReadCacheStore = %+v, want the zero store", got)
		}
	})
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

func TestAdoptDeploymentsStore(t *testing.T) {
	t.Run("preview stores separately", func(t *testing.T) {
		ssmc := newFakeSSM()
		preview := offeredDeploymentsStore()
		preview[edge.OfferKeyStoreEndpoint] = "https://ocel-deployments-store-preview.acct.workers.dev"
		preview[edge.OfferKeyStoreScriptName] = "ocel-deployments-store-preview"
		preview[edge.OfferKeyStoreBootstrapCred] = "cred-preview"

		if err := adoptDeploymentsStore(context.Background(), ssmc, ClassProduction, "fake", offeredDeploymentsStore()); err != nil {
			t.Fatalf("production adopt: %v", err)
		}
		if err := adoptDeploymentsStore(context.Background(), ssmc, ClassPreview, "fake", preview); err != nil {
			t.Fatalf("preview adopt: %v", err)
		}

		prod, err := ReadDeploymentsStoreFor(context.Background(), ssmc, ClassProduction)
		if err != nil {
			t.Fatalf("ReadDeploymentsStoreFor(production): %v", err)
		}
		prev, err := ReadDeploymentsStoreFor(context.Background(), ssmc, ClassPreview)
		if err != nil {
			t.Fatalf("ReadDeploymentsStoreFor(preview): %v", err)
		}
		wantProd := DeploymentsStore{Endpoint: "https://ocel-deployments-store.acct.workers.dev", ScriptName: "ocel-deployments-store", BootstrapCred: "cred-prod"}
		wantPrev := DeploymentsStore{Endpoint: "https://ocel-deployments-store-preview.acct.workers.dev", ScriptName: "ocel-deployments-store-preview", BootstrapCred: "cred-preview"}
		if prod != wantProd {
			t.Errorf("production store = %+v, want %+v", prod, wantProd)
		}
		if prev != wantPrev {
			t.Errorf("preview store = %+v, want %+v", prev, wantPrev)
		}
	})
}

func TestAdoptDeploymentsStoreBacksTheOmittedCredentialOutOfSSM(t *testing.T) {
	t.Run("a standing credential survives a re-offer without one", func(t *testing.T) {
		ssmc := newFakeSSM()
		if err := adoptDeploymentsStore(context.Background(), ssmc, ClassProduction, "fake", offeredDeploymentsStore()); err != nil {
			t.Fatalf("first adopt: %v", err)
		}

		reoffer := offeredDeploymentsStore()
		delete(reoffer, edge.OfferKeyStoreBootstrapCred)
		reoffer[edge.OfferKeyStoreEndpoint] = "https://ocel-deployments-store.acct.workers.dev/v2"
		if err := adoptDeploymentsStore(context.Background(), ssmc, ClassProduction, "fake", reoffer); err != nil {
			t.Fatalf("second adopt: %v", err)
		}

		got, err := ReadDeploymentsStoreFor(context.Background(), ssmc, ClassProduction)
		if err != nil {
			t.Fatalf("ReadDeploymentsStoreFor: %v", err)
		}
		if got.BootstrapCred != "cred-prod" {
			t.Errorf("credential = %q, want the stored one kept where the edge offered none", got.BootstrapCred)
		}
		if got.Endpoint != "https://ocel-deployments-store.acct.workers.dev/v2" {
			t.Errorf("endpoint = %q, want the reoffered coordinate", got.Endpoint)
		}
	})

	t.Run("nothing on either side is a refusal, not an empty credential", func(t *testing.T) {
		ssmc := newFakeSSM()
		offer := offeredDeploymentsStore()
		delete(offer, edge.OfferKeyStoreBootstrapCred)

		err := adoptDeploymentsStore(context.Background(), ssmc, ClassProduction, "fake", offer)
		if err == nil {
			t.Fatal("adoptDeploymentsStore stored a credential-less store rather than refusing")
		}
		if !strings.Contains(err.Error(), "ocel-deployments-store") || !strings.Contains(err.Error(), DeploymentsStoreParamName) {
			t.Errorf("error = %v, want it to name the worker and the parameter that holds nothing", err)
		}
		if _, stored := ssmc.params[DeploymentsStoreParamName]; stored {
			t.Error("a credential-less store was written despite the refusal")
		}
	})
}

func TestAdoptISRWriterBacksTheOmittedCredentialOutOfSSM(t *testing.T) {
	t.Run("a standing credential survives a re-offer without one", func(t *testing.T) {
		ssmc := newFakeSSM()
		if err := adoptISRWriter(context.Background(), ssmc, ClassProduction, "fake", offeredISRWriter("", "cred-prod")); err != nil {
			t.Fatalf("first adopt: %v", err)
		}

		if err := adoptISRWriter(context.Background(), ssmc, ClassProduction, "fake", offeredISRWriter("", "")); err != nil {
			t.Fatalf("second adopt: %v", err)
		}
		got, err := ReadISRWriterFor(context.Background(), ssmc, ClassProduction)
		if err != nil {
			t.Fatalf("ReadISRWriterFor: %v", err)
		}
		if got.BootstrapCred != "cred-prod" {
			t.Errorf("credential = %q, want the stored one kept where the edge offered none", got.BootstrapCred)
		}
	})

	t.Run("nothing on either side is a refusal, not an empty credential", func(t *testing.T) {
		ssmc := newFakeSSM()

		err := adoptISRWriter(context.Background(), ssmc, ClassProduction, "fake", offeredISRWriter("", ""))
		if err == nil {
			t.Fatal("adoptISRWriter stored a credential-less writer rather than refusing")
		}
		if !strings.Contains(err.Error(), "ocel-isr-writer") || !strings.Contains(err.Error(), ISRWriterParamName) {
			t.Errorf("error = %v, want it to name the worker and the parameter that holds nothing", err)
		}
		if _, stored := ssmc.params[ISRWriterParamName]; stored {
			t.Error("a credential-less writer was written despite the refusal")
		}
	})
}

func TestReadDeploymentsStore(t *testing.T) {
	t.Run("absent is not an error", func(t *testing.T) {
		for _, class := range []string{ClassProduction, ClassPreview} {
			got, err := ReadDeploymentsStoreFor(context.Background(), newFakeSSM(), class)
			if err != nil {
				t.Fatalf("ReadDeploymentsStoreFor(%q) on an absent parameter: %v", class, err)
			}
			if got != (DeploymentsStore{}) {
				t.Errorf("ReadDeploymentsStoreFor(%q) = %+v, want the zero store", class, got)
			}
		}
	})
}

func offeredISRWriter(suffix, cred string) map[string]string {
	return map[string]string{
		edge.OfferKeyISRWriterEndpoint:      "https://ocel-isr-writer" + suffix + ".acct.workers.dev",
		edge.OfferKeyISRWriterScriptName:    "ocel-isr-writer" + suffix,
		edge.OfferKeyISRWriterBootstrapCred: cred,
	}
}

func TestISRWriterParamFor(t *testing.T) {
	for _, tc := range []struct {
		class string
		want  string
	}{
		{ClassProduction, ISRWriterParamName},
		{ClassPreview, ISRWriterPreviewParamName},
	} {
		got, err := ISRWriterParamFor(tc.class)
		if err != nil {
			t.Fatalf("ISRWriterParamFor(%q): %v", tc.class, err)
		}
		if got != tc.want {
			t.Errorf("ISRWriterParamFor(%q) = %q, want %q", tc.class, got, tc.want)
		}
	}
	if _, err := ISRWriterParamFor("nonsense"); err == nil {
		t.Error("ISRWriterParamFor(unknown class) = nil error, want an error")
	}
	if ISRWriterParamName == ISRWriterPreviewParamName {
		t.Error("production and preview isr-writer parameters must differ")
	}
}

func TestAdoptISRWriter(t *testing.T) {
	t.Run("preview stores separately", func(t *testing.T) {
		ssmc := newFakeSSM()
		if err := adoptISRWriter(context.Background(), ssmc, ClassProduction, "fake", offeredISRWriter("", "cred-prod")); err != nil {
			t.Fatalf("production adopt: %v", err)
		}
		if err := adoptISRWriter(context.Background(), ssmc, ClassPreview, "fake", offeredISRWriter("-preview", "cred-preview")); err != nil {
			t.Fatalf("preview adopt: %v", err)
		}

		prod, err := ReadISRWriterFor(context.Background(), ssmc, ClassProduction)
		if err != nil {
			t.Fatalf("ReadISRWriterFor(production): %v", err)
		}
		prev, err := ReadISRWriterFor(context.Background(), ssmc, ClassPreview)
		if err != nil {
			t.Fatalf("ReadISRWriterFor(preview): %v", err)
		}
		wantProd := ISRWriter{Endpoint: "https://ocel-isr-writer.acct.workers.dev", ScriptName: "ocel-isr-writer", BootstrapCred: "cred-prod"}
		wantPrev := ISRWriter{Endpoint: "https://ocel-isr-writer-preview.acct.workers.dev", ScriptName: "ocel-isr-writer-preview", BootstrapCred: "cred-preview"}
		if prod != wantProd {
			t.Errorf("production writer = %+v, want %+v", prod, wantProd)
		}
		if prev != wantPrev {
			t.Errorf("preview writer = %+v, want %+v", prev, wantPrev)
		}
	})
}

func TestReadISRWriterFor(t *testing.T) {
	t.Run("absent is not an error", func(t *testing.T) {
		got, err := ReadISRWriterFor(context.Background(), newFakeSSM(), ClassProduction)
		if err != nil {
			t.Fatalf("ReadISRWriterFor on an absent parameter: %v", err)
		}
		if got != (ISRWriter{}) {
			t.Errorf("ReadISRWriterFor = %+v, want the zero writer", got)
		}
	})
}

func TestEnsureISRWriterSeed(t *testing.T) {
	t.Run("is create only", func(t *testing.T) {
		ssmc := newFakeSSM()

		first, err := ensureISRWriterSeed(context.Background(), ssmc, ClassProduction)
		if err != nil {
			t.Fatalf("ensureISRWriterSeed: %v", err)
		}
		if first == "" {
			t.Fatal("ensureISRWriterSeed minted no seed")
		}
		again, err := ensureISRWriterSeed(context.Background(), ssmc, ClassProduction)
		if err != nil {
			t.Fatalf("ensureISRWriterSeed (second run): %v", err)
		}
		if again != first {
			t.Errorf("second bootstrap returned seed %q, want the stored %q", again, first)
		}

		preview, err := ensureISRWriterSeed(context.Background(), ssmc, ClassPreview)
		if err != nil {
			t.Fatalf("ensureISRWriterSeed (preview): %v", err)
		}
		if preview == first {
			t.Error("preview and production share a seed; each bootstrap has its own writer")
		}
	})

	t.Run("converges on a concurrent bootstrap", func(t *testing.T) {
		ssmc := &racingSSM{fakeSSM: newFakeSSM(), winner: "the-other-bootstraps-seed"}

		seed, err := ensureISRWriterSeed(context.Background(), ssmc, ClassProduction)
		if err != nil {
			t.Fatalf("ensureISRWriterSeed lost a race instead of converging: %v", err)
		}
		if seed != ssmc.winner {
			t.Errorf("seed = %q, want the winner's %q", seed, ssmc.winner)
		}
	})
}

type racingSSM struct {
	*fakeSSM
	winner string
	raced  bool
}

func (r *racingSSM) PutParameter(ctx context.Context, in *ssm.PutParameterInput, opts ...func(*ssm.Options)) (*ssm.PutParameterOutput, error) {
	if !r.raced {
		r.raced = true
		r.fakeSSM.params[aws.ToString(in.Name)] = r.winner
	}
	return r.fakeSSM.PutParameter(ctx, in, opts...)
}

func TestReadISRWriterSeedFor(t *testing.T) {
	t.Run("absent is not a failure", func(t *testing.T) {
		seed, err := ReadISRWriterSeedFor(context.Background(), newFakeSSM(), ClassProduction)
		if err != nil {
			t.Fatalf("ReadISRWriterSeedFor: %v", err)
		}
		if seed != "" {
			t.Errorf("seed = %q, want empty", seed)
		}
	})
}
