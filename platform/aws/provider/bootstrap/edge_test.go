package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

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
	batches      int
	wanted       []string
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

func (f *fakeSSM) GetParameters(_ context.Context, in *ssm.GetParametersInput, _ ...func(*ssm.Options)) (*ssm.GetParametersOutput, error) {
	f.batches++
	out := &ssm.GetParametersOutput{}
	for _, name := range in.Names {
		f.wanted = append(f.wanted, name)
		v, ok := f.params[name]
		if !ok {
			out.InvalidParameters = append(out.InvalidParameters, name)
			continue
		}
		out.Parameters = append(out.Parameters, ssmtypes.Parameter{Name: aws.String(name), Value: aws.String(v)})
	}
	return out, nil
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
	created  []string
	keys     []string
	deleted  []string
	minted   map[string]time.Time
	lastUsed map[string]time.Time
}

var mintedAt = time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)

func (f *fakeIAM) GetAccessKeyLastUsed(_ context.Context, in *iam.GetAccessKeyLastUsedInput, _ ...func(*iam.Options)) (*iam.GetAccessKeyLastUsedOutput, error) {
	out := &iam.GetAccessKeyLastUsedOutput{AccessKeyLastUsed: &iamtypes.AccessKeyLastUsed{}}
	if at, ok := f.lastUsed[aws.ToString(in.AccessKeyId)]; ok {
		out.AccessKeyLastUsed.LastUsedDate = aws.Time(at)
	}
	return out, nil
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
		key := iamtypes.AccessKeyMetadata{UserName: in.UserName, AccessKeyId: aws.String(id)}
		if at, ok := f.minted[id]; ok {
			key.CreateDate = aws.Time(at)
		}
		meta = append(meta, key)
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

		outcome, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, defaultNamespace, ClassProduction, KindCloudflare, mintedAt)
		if err != nil {
			t.Fatalf("ensureEdgeCredentials: %v", err)
		}
		if !outcome.minted {
			t.Error("expected a mint on first run")
		}
		if len(iamc.created) != 1 || iamc.created[0] != edgeUserName {
			t.Errorf("CreateAccessKey users = %v, want [%s]", iamc.created, edgeUserName)
		}

		stored, ok := ssmc.params[cloudflareNames(ClassProduction).credentialsParam]
		if !ok {
			t.Fatalf("credentials were not written to %s", cloudflareNames(ClassProduction).credentialsParam)
		}
		var creds EdgeCredentials
		if err := json.Unmarshal([]byte(stored), &creds); err != nil {
			t.Fatalf("stored value is not EdgeCredentials JSON: %v", err)
		}
		if creds.AccessKeyID != "AKIAEDGE" || creds.SecretAccessKey != "secret-edge" {
			t.Errorf("stored creds = %+v, want the minted key", creds)
		}
		if !creds.CreatedAt.Equal(mintedAt) {
			t.Errorf("stored CreatedAt = %v, want the mint time %v, which is what a deploy ages the key by", creds.CreatedAt, mintedAt)
		}
	})

	t.Run("reuses a recorded key the user still has", func(t *testing.T) {
		ssmc := newFakeSSM()
		ssmc.params[cloudflareNames(ClassProduction).credentialsParam] = `{"accessKeyId":"AKOLD","secretAccessKey":"old"}`
		iamc := &fakeIAM{keys: []string{"AKOLD"}}

		outcome, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, defaultNamespace, ClassProduction, KindCloudflare, mintedAt)
		if err != nil {
			t.Fatalf("ensureEdgeCredentials: %v", err)
		}
		if outcome.minted {
			t.Error("expected no mint when the recorded key is still live")
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
		ssmc.params[cloudflareNames(ClassProduction).credentialsParam] = `{"accessKeyId":"AKGONE","secretAccessKey":"gone"}`
		iamc := &fakeIAM{}

		outcome, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, defaultNamespace, ClassProduction, KindCloudflare, mintedAt)
		if err != nil {
			t.Fatalf("ensureEdgeCredentials: %v", err)
		}
		if !outcome.minted {
			t.Error("expected a mint: a CloudFormation replacement of the user takes its key, and the parameter outlives it")
		}
		var creds EdgeCredentials
		if err := json.Unmarshal([]byte(ssmc.params[cloudflareNames(ClassProduction).credentialsParam]), &creds); err != nil {
			t.Fatalf("stored value is not EdgeCredentials JSON: %v", err)
		}
		if creds.AccessKeyID != "AKIAEDGE" {
			t.Errorf("stored key = %q, want the freshly minted AKIAEDGE: the edge signs with a key AWS no longer knows otherwise", creds.AccessKeyID)
		}
	})

	t.Run("names the dead key when the cap blocks a re-mint", func(t *testing.T) {
		ssmc := newFakeSSM()
		ssmc.params[cloudflareNames(ClassProduction).credentialsParam] = `{"accessKeyId":"AKGONE","secretAccessKey":"gone"}`
		iamc := &fakeIAM{keys: []string{"AK1", "AK2"}}

		_, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, defaultNamespace, ClassProduction, KindCloudflare, mintedAt)
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

		_, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, defaultNamespace, ClassProduction, KindCloudflare, mintedAt)
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

		if _, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, defaultNamespace, ClassPreview, KindCloudflare, mintedAt); err != nil {
			t.Fatalf("ensureEdgeCredentials: %v", err)
		}
		if len(iamc.created) != 1 || iamc.created[0] != previewEdgeUser {
			t.Errorf("CreateAccessKey users = %v, want [%s]", iamc.created, previewEdgeUser)
		}
		if _, ok := ssmc.params[cloudflareNames(ClassPreview).credentialsParam]; !ok {
			t.Errorf("preview credentials were not written to %s", cloudflareNames(ClassPreview).credentialsParam)
		}
	})
}

func recordedCreds(t *testing.T, ssmc *fakeSSM) EdgeCredentials {
	t.Helper()
	var creds EdgeCredentials
	if err := json.Unmarshal([]byte(ssmc.params[cloudflareNames(ClassProduction).credentialsParam]), &creds); err != nil {
		t.Fatalf("stored value is not EdgeCredentials JSON: %v", err)
	}
	return creds
}

func TestEdgeCredentialRotation(t *testing.T) {
	stale := mintedAt.Add(EdgeKeyMaxAge)
	fresh := mintedAt.Add(EdgeKeyMaxAge - time.Hour)
	recorded := func(ssmc *fakeSSM, id string, at time.Time) {
		payload, _ := json.Marshal(EdgeCredentials{AccessKeyID: id, SecretAccessKey: "s", CreatedAt: at})
		ssmc.params[cloudflareNames(ClassProduction).credentialsParam] = string(payload)
	}

	t.Run("a key younger than the maximum age is kept", func(t *testing.T) {
		ssmc := newFakeSSM()
		recorded(ssmc, "AKOLD", mintedAt)
		iamc := &fakeIAM{keys: []string{"AKOLD"}}

		outcome, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, defaultNamespace, ClassProduction, KindCloudflare, fresh)
		if err != nil {
			t.Fatalf("ensureEdgeCredentials: %v", err)
		}
		if outcome != (edgeKeyOutcome{}) {
			t.Errorf("outcome = %+v, want nothing minted, rotated or retired", outcome)
		}
		if len(iamc.created) != 0 || len(iamc.deleted) != 0 || ssmc.puts != 0 {
			t.Errorf("created %v, deleted %v, puts %d; want the account untouched", iamc.created, iamc.deleted, ssmc.puts)
		}
	})

	t.Run("a key at the maximum age is rotated: a fresh key is minted and recorded, the old one stays for the workers still holding it", func(t *testing.T) {
		ssmc := newFakeSSM()
		recorded(ssmc, "AKOLD", mintedAt)
		iamc := &fakeIAM{keys: []string{"AKOLD"}}

		outcome, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, defaultNamespace, ClassProduction, KindCloudflare, stale)
		if err != nil {
			t.Fatalf("ensureEdgeCredentials: %v", err)
		}
		if !outcome.rotated || !outcome.minted || outcome.retired != "" {
			t.Errorf("outcome = %+v, want a rotation that retires nothing yet", outcome)
		}
		if creds := recordedCreds(t, ssmc); creds.AccessKeyID != "AKIAEDGE" || !creds.CreatedAt.Equal(stale) {
			t.Errorf("recorded = %+v, want the fresh key stamped with the rotation time", creds)
		}
		if len(iamc.deleted) != 0 {
			t.Errorf("deleted %v, want the old key left for every worker still signing with it", iamc.deleted)
		}
		if !slices.Equal(iamc.keys, []string{"AKOLD", "AKIAEDGE"}) {
			t.Errorf("keys = %v, want both generations live", iamc.keys)
		}
	})

	t.Run("the age falls back to what IAM recorded when the parameter carries none", func(t *testing.T) {
		ssmc := newFakeSSM()
		ssmc.params[cloudflareNames(ClassProduction).credentialsParam] = `{"accessKeyId":"AKOLD","secretAccessKey":"s"}`
		iamc := &fakeIAM{keys: []string{"AKOLD"}, minted: map[string]time.Time{"AKOLD": mintedAt}}

		outcome, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, defaultNamespace, ClassProduction, KindCloudflare, stale)
		if err != nil {
			t.Fatalf("ensureEdgeCredentials: %v", err)
		}
		if !outcome.rotated {
			t.Errorf("outcome = %+v, want a rotation from IAM's own CreateDate", outcome)
		}
	})

	t.Run("the next bootstrap retires the superseded key once nothing has signed with it for a day", func(t *testing.T) {
		ssmc := newFakeSSM()
		recorded(ssmc, "AKNEW", stale)
		iamc := &fakeIAM{
			keys:     []string{"AKOLD", "AKNEW"},
			lastUsed: map[string]time.Time{"AKOLD": stale.Add(2 * time.Hour)},
		}

		outcome, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, defaultNamespace, ClassProduction, KindCloudflare, stale.Add(48*time.Hour))
		if err != nil {
			t.Fatalf("ensureEdgeCredentials: %v", err)
		}
		if outcome.retired != "AKOLD" || outcome.minted {
			t.Errorf("outcome = %+v, want the idle old key retired and nothing minted", outcome)
		}
		if !slices.Equal(iamc.deleted, []string{"AKOLD"}) || !slices.Equal(iamc.keys, []string{"AKNEW"}) {
			t.Errorf("deleted %v, keys %v; want only AKOLD gone", iamc.deleted, iamc.keys)
		}
	})

	t.Run("a superseded key a worker signed with today is left standing", func(t *testing.T) {
		ssmc := newFakeSSM()
		recorded(ssmc, "AKNEW", stale)
		now := stale.Add(48 * time.Hour)
		iamc := &fakeIAM{
			keys:     []string{"AKOLD", "AKNEW"},
			lastUsed: map[string]time.Time{"AKOLD": now.Add(-time.Hour)},
		}

		outcome, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, defaultNamespace, ClassProduction, KindCloudflare, now)
		if err != nil {
			t.Fatalf("ensureEdgeCredentials: %v", err)
		}
		if outcome.retired != "" || len(iamc.deleted) != 0 {
			t.Errorf("outcome = %+v, deleted %v; want a key still in use kept until every worker has moved off it", outcome, iamc.deleted)
		}
	})

	t.Run("a never-used superseded key is retired at once", func(t *testing.T) {
		ssmc := newFakeSSM()
		recorded(ssmc, "AKNEW", stale)
		iamc := &fakeIAM{keys: []string{"AKOLD", "AKNEW"}}

		outcome, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, defaultNamespace, ClassProduction, KindCloudflare, stale.Add(time.Hour))
		if err != nil {
			t.Fatalf("ensureEdgeCredentials: %v", err)
		}
		if outcome.retired != "AKOLD" {
			t.Errorf("outcome = %+v, want the never-used key retired", outcome)
		}
	})

	t.Run("a rotation with no room refuses and names the key still in use", func(t *testing.T) {
		ssmc := newFakeSSM()
		recorded(ssmc, "AKNEW", mintedAt)
		now := mintedAt.Add(EdgeKeyMaxAge)
		iamc := &fakeIAM{
			keys:     []string{"AKOLD", "AKNEW"},
			lastUsed: map[string]time.Time{"AKOLD": now.Add(-time.Minute)},
		}

		_, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, defaultNamespace, ClassProduction, KindCloudflare, now)
		if err == nil {
			t.Fatal("ensureEdgeCredentials err = nil, want a refusal: two keys stand and both are in use")
		}
		if !strings.Contains(err.Error(), "AKNEW") {
			t.Errorf("err = %v, want it to name the key every project must move onto", err)
		}
		if len(iamc.created) != 0 || len(iamc.deleted) != 0 {
			t.Errorf("created %v, deleted %v; want the account untouched by a refused rotation", iamc.created, iamc.deleted)
		}
	})

	t.Run("a rotation whose predecessor went idle retires it and rotates in one run", func(t *testing.T) {
		ssmc := newFakeSSM()
		recorded(ssmc, "AKNEW", mintedAt)
		now := mintedAt.Add(EdgeKeyMaxAge)
		iamc := &fakeIAM{keys: []string{"AKOLD", "AKNEW"}}

		outcome, err := ensureEdgeCredentials(context.Background(), iamc, ssmc, defaultNamespace, ClassProduction, KindCloudflare, now)
		if err != nil {
			t.Fatalf("ensureEdgeCredentials: %v", err)
		}
		if outcome.retired != "AKOLD" || !outcome.rotated {
			t.Errorf("outcome = %+v, want AKOLD retired and AKNEW succeeded", outcome)
		}
		if !slices.Equal(iamc.keys, []string{"AKNEW", "AKIAEDGE"}) {
			t.Errorf("keys = %v, want the two newest generations", iamc.keys)
		}
	})
}

func TestStaleEdgeKeyNotice(t *testing.T) {
	creds := EdgeCredentials{AccessKeyID: "AKOLD", CreatedAt: mintedAt}

	if notice := StaleEdgeKeyNotice(creds, mintedAt.Add(EdgeKeyMaxAge-time.Second), ClassProduction); notice != "" {
		t.Errorf("notice = %q, want none for a key within its age", notice)
	}
	notice := StaleEdgeKeyNotice(creds, mintedAt.Add(EdgeKeyMaxAge+24*time.Hour), ClassProduction)
	for _, want := range []string{"AKOLD", "91 days", "ocel bootstrap"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice = %q, want it to carry %q", notice, want)
		}
	}
	if notice := StaleEdgeKeyNotice(EdgeCredentials{AccessKeyID: "AKOLD"}, mintedAt.Add(10*EdgeKeyMaxAge), ClassProduction); notice != "" {
		t.Errorf("notice = %q, want none when the mint time is unknown", notice)
	}
}

func TestReadEdgeCredentials(t *testing.T) {
	ssmc := newFakeSSM()
	ssmc.params[cloudflareNames(ClassProduction).credentialsParam] = `{"accessKeyId":"AK1","secretAccessKey":"s1"}`

	creds, err := ReadEdgeCredentials(context.Background(), ssmc, defaultNamespace, ClassProduction, KindCloudflare)
	if err != nil {
		t.Fatalf("ReadEdgeCredentials: %v", err)
	}
	if creds.AccessKeyID != "AK1" || creds.SecretAccessKey != "s1" {
		t.Errorf("creds = %+v, want AK1/s1", creds)
	}
}

func TestEdgeCredentials(t *testing.T) {
	t.Run("unknown class", func(t *testing.T) {
		if _, err := ensureEdgeCredentials(context.Background(), &fakeIAM{}, newFakeSSM(), defaultNamespace, "nonsense", KindCloudflare, mintedAt); err == nil {
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

		if err := adoptCacheStore(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake", offeredStore()); err != nil {
			t.Fatalf("adoptCacheStore: %v", err)
		}
		got, err := ReadCacheStore(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake")
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
		if err := adoptCacheStore(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake", offeredStore()); err != nil {
			t.Fatalf("first adopt: %v", err)
		}

		reoffer := offeredStore()
		delete(reoffer, edge.OfferKeySecretAccessKey)
		reoffer[edge.OfferKeyEndpoint] = "https://acct.r2.cloudflarestorage.com/v2"

		if err := adoptCacheStore(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake", reoffer); err != nil {
			t.Fatalf("second adopt: %v", err)
		}
		got, err := ReadCacheStore(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake")
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
					ssmc.params[namesFor(ClassProduction, "fake").cacheStoreParam] = tc.stored
				}
				offer := offeredStore()
				delete(offer, edge.OfferKeySecretAccessKey)

				err := adoptCacheStore(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake", offer)
				if err == nil {
					t.Fatal("expected a dangling-token error for a secretless offer with no matching stored secret")
				}
				if !strings.Contains(err.Error(), "tok-1") {
					t.Errorf("diagnostic does not name the token: %v", err)
				}
				if ssmc.params[namesFor(ClassProduction, "fake").cacheStoreParam] != tc.stored {
					t.Errorf("wrote %q over the stored store despite failing", ssmc.params[namesFor(ClassProduction, "fake").cacheStoreParam])
				}
			})
		}
	})

	t.Run("preview stores separately", func(t *testing.T) {
		ssmc := newFakeSSM()
		preview := offeredStore()
		preview[edge.OfferKeyBucket] = "ocel-edge-cache-preview"
		preview[edge.OfferKeyAccessKeyID] = "tok-preview"

		if err := adoptCacheStore(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake", offeredStore()); err != nil {
			t.Fatalf("production adopt: %v", err)
		}
		if err := adoptCacheStore(context.Background(), ssmc, defaultNamespace, ClassPreview, "fake", preview); err != nil {
			t.Fatalf("preview adopt: %v", err)
		}

		prod, err := ReadCacheStore(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake")
		if err != nil {
			t.Fatalf("ReadCacheStore production: %v", err)
		}
		prev, err := ReadCacheStore(context.Background(), ssmc, defaultNamespace, ClassPreview, "fake")
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
		if err := adoptCacheStore(context.Background(), newFakeSSM(), defaultNamespace, "nonsense", "fake", offeredStore()); err == nil {
			t.Error("expected an error for an unknown class")
		}
	})
}

func TestReadCacheStore(t *testing.T) {
	t.Run("absent is not an error", func(t *testing.T) {
		got, err := ReadCacheStore(context.Background(), newFakeSSM(), defaultNamespace, ClassProduction, "fake")
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
		{ClassProduction, "/ocel/edge/fake/deployments-store"},
		{ClassPreview, "/ocel/edge/fake-preview/deployments-store"},
	} {
		got, err := DeploymentsStoreParamFor(defaultNamespace, tc.class, "fake")
		if err != nil {
			t.Fatalf("DeploymentsStoreParamFor(defaultNamespace, %q): %v", tc.class, err)
		}
		if got != tc.want {
			t.Errorf("DeploymentsStoreParamFor(defaultNamespace, %q) = %q, want %q", tc.class, got, tc.want)
		}
	}
	if _, err := DeploymentsStoreParamFor(defaultNamespace, "nonsense", "fake"); err == nil {
		t.Error("DeploymentsStoreParamFor(defaultNamespace, unknown class) = nil error, want an error")
	}
	if _, err := DeploymentsStoreParamFor(defaultNamespace, ClassProduction, ""); err == nil {
		t.Error("DeploymentsStoreParamFor(defaultNamespace, no kind) = nil error, want an error: every edge parameter is namespaced by the kind that owns it")
	}
}

func TestAdoptDeploymentsStore(t *testing.T) {
	t.Run("preview stores separately", func(t *testing.T) {
		ssmc := newFakeSSM()
		preview := offeredDeploymentsStore()
		preview[edge.OfferKeyStoreEndpoint] = "https://ocel-deployments-store-preview.acct.workers.dev"
		preview[edge.OfferKeyStoreScriptName] = "ocel-deployments-store-preview"
		preview[edge.OfferKeyStoreBootstrapCred] = "cred-preview"

		if err := adoptDeploymentsStore(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake", offeredDeploymentsStore()); err != nil {
			t.Fatalf("production adopt: %v", err)
		}
		if err := adoptDeploymentsStore(context.Background(), ssmc, defaultNamespace, ClassPreview, "fake", preview); err != nil {
			t.Fatalf("preview adopt: %v", err)
		}

		prod, err := ReadDeploymentsStoreFor(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake")
		if err != nil {
			t.Fatalf("ReadDeploymentsStoreFor(production): %v", err)
		}
		prev, err := ReadDeploymentsStoreFor(context.Background(), ssmc, defaultNamespace, ClassPreview, "fake")
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
		if err := adoptDeploymentsStore(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake", offeredDeploymentsStore()); err != nil {
			t.Fatalf("first adopt: %v", err)
		}

		reoffer := offeredDeploymentsStore()
		delete(reoffer, edge.OfferKeyStoreBootstrapCred)
		reoffer[edge.OfferKeyStoreEndpoint] = "https://ocel-deployments-store.acct.workers.dev/v2"
		if err := adoptDeploymentsStore(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake", reoffer); err != nil {
			t.Fatalf("second adopt: %v", err)
		}

		got, err := ReadDeploymentsStoreFor(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake")
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

		err := adoptDeploymentsStore(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake", offer)
		if err == nil {
			t.Fatal("adoptDeploymentsStore stored a credential-less store rather than refusing")
		}
		if !strings.Contains(err.Error(), "ocel-deployments-store") || !strings.Contains(err.Error(), namesFor(ClassProduction, "fake").deploymentsStoreParam) {
			t.Errorf("error = %v, want it to name the worker and the parameter that holds nothing", err)
		}
		if _, stored := ssmc.params[namesFor(ClassProduction, "fake").deploymentsStoreParam]; stored {
			t.Error("a credential-less store was written despite the refusal")
		}
	})
}

func TestAdoptISRWriterBacksTheOmittedCredentialOutOfSSM(t *testing.T) {
	t.Run("a standing credential survives a re-offer without one", func(t *testing.T) {
		ssmc := newFakeSSM()
		if err := adoptISRWriter(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake", offeredISRWriter("", "cred-prod")); err != nil {
			t.Fatalf("first adopt: %v", err)
		}

		if err := adoptISRWriter(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake", offeredISRWriter("", "")); err != nil {
			t.Fatalf("second adopt: %v", err)
		}
		got, err := ReadISRWriterFor(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake")
		if err != nil {
			t.Fatalf("ReadISRWriterFor: %v", err)
		}
		if got.BootstrapCred != "cred-prod" {
			t.Errorf("credential = %q, want the stored one kept where the edge offered none", got.BootstrapCred)
		}
	})

	t.Run("nothing on either side is a refusal, not an empty credential", func(t *testing.T) {
		ssmc := newFakeSSM()

		err := adoptISRWriter(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake", offeredISRWriter("", ""))
		if err == nil {
			t.Fatal("adoptISRWriter stored a credential-less writer rather than refusing")
		}
		if !strings.Contains(err.Error(), "ocel-isr-writer") || !strings.Contains(err.Error(), namesFor(ClassProduction, "fake").isrWriterParam) {
			t.Errorf("error = %v, want it to name the worker and the parameter that holds nothing", err)
		}
		if _, stored := ssmc.params[namesFor(ClassProduction, "fake").isrWriterParam]; stored {
			t.Error("a credential-less writer was written despite the refusal")
		}
	})
}

func TestReadDeploymentsStore(t *testing.T) {
	t.Run("absent is not an error", func(t *testing.T) {
		for _, class := range []string{ClassProduction, ClassPreview} {
			got, err := ReadDeploymentsStoreFor(context.Background(), newFakeSSM(), defaultNamespace, class, "fake")
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
		{ClassProduction, "/ocel/edge/fake/isr-writer"},
		{ClassPreview, "/ocel/edge/fake-preview/isr-writer"},
	} {
		got, err := ISRWriterParamFor(defaultNamespace, tc.class, "fake")
		if err != nil {
			t.Fatalf("ISRWriterParamFor(defaultNamespace, %q): %v", tc.class, err)
		}
		if got != tc.want {
			t.Errorf("ISRWriterParamFor(defaultNamespace, %q) = %q, want %q", tc.class, got, tc.want)
		}
	}
	if _, err := ISRWriterParamFor(defaultNamespace, "nonsense", "fake"); err == nil {
		t.Error("ISRWriterParamFor(defaultNamespace, unknown class) = nil error, want an error")
	}
	if _, err := ISRWriterParamFor(defaultNamespace, ClassProduction, ""); err == nil {
		t.Error("ISRWriterParamFor(defaultNamespace, no kind) = nil error, want an error: every edge parameter is namespaced by the kind that owns it")
	}
}

func TestAdoptISRWriter(t *testing.T) {
	t.Run("preview stores separately", func(t *testing.T) {
		ssmc := newFakeSSM()
		if err := adoptISRWriter(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake", offeredISRWriter("", "cred-prod")); err != nil {
			t.Fatalf("production adopt: %v", err)
		}
		if err := adoptISRWriter(context.Background(), ssmc, defaultNamespace, ClassPreview, "fake", offeredISRWriter("-preview", "cred-preview")); err != nil {
			t.Fatalf("preview adopt: %v", err)
		}

		prod, err := ReadISRWriterFor(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake")
		if err != nil {
			t.Fatalf("ReadISRWriterFor(production): %v", err)
		}
		prev, err := ReadISRWriterFor(context.Background(), ssmc, defaultNamespace, ClassPreview, "fake")
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
		got, err := ReadISRWriterFor(context.Background(), newFakeSSM(), defaultNamespace, ClassProduction, "fake")
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

		first, err := ensureISRWriterSeed(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake")
		if err != nil {
			t.Fatalf("ensureISRWriterSeed: %v", err)
		}
		if first == "" {
			t.Fatal("ensureISRWriterSeed minted no seed")
		}
		again, err := ensureISRWriterSeed(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake")
		if err != nil {
			t.Fatalf("ensureISRWriterSeed (second run): %v", err)
		}
		if again != first {
			t.Errorf("second bootstrap returned seed %q, want the stored %q", again, first)
		}

		preview, err := ensureISRWriterSeed(context.Background(), ssmc, defaultNamespace, ClassPreview, "fake")
		if err != nil {
			t.Fatalf("ensureISRWriterSeed (preview): %v", err)
		}
		if preview == first {
			t.Error("preview and production share a seed; each bootstrap has its own writer")
		}
	})

	t.Run("converges on a concurrent bootstrap", func(t *testing.T) {
		ssmc := &racingSSM{fakeSSM: newFakeSSM(), winner: "the-other-bootstraps-seed"}

		seed, err := ensureISRWriterSeed(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake")
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
		r.params[aws.ToString(in.Name)] = r.winner
	}
	return r.fakeSSM.PutParameter(ctx, in, opts...)
}

func TestReadISRWriterSeedFor(t *testing.T) {
	t.Run("absent is not a failure", func(t *testing.T) {
		seed, err := ReadISRWriterSeedFor(context.Background(), newFakeSSM(), defaultNamespace, ClassProduction, "fake")
		if err != nil {
			t.Fatalf("ReadISRWriterSeedFor: %v", err)
		}
		if seed != "" {
			t.Errorf("seed = %q, want empty", seed)
		}
	})
}

type adoption struct {
	what  string
	first func(context.Context, SSMAPI) error
	again func(context.Context, SSMAPI) error
}

func adoptions() []adoption {
	cacheStore := func(offer map[string]string) func(context.Context, SSMAPI) error {
		return func(ctx context.Context, ssmc SSMAPI) error {
			return adoptCacheStore(ctx, ssmc, defaultNamespace, ClassProduction, "fake", offer)
		}
	}
	deploymentsStore := func(offer map[string]string) func(context.Context, SSMAPI) error {
		return func(ctx context.Context, ssmc SSMAPI) error {
			return adoptDeploymentsStore(ctx, ssmc, defaultNamespace, ClassProduction, "fake", offer)
		}
	}
	isrWriter := func(offer map[string]string) func(context.Context, SSMAPI) error {
		return func(ctx context.Context, ssmc SSMAPI) error {
			return adoptISRWriter(ctx, ssmc, defaultNamespace, ClassProduction, "fake", offer)
		}
	}
	edgeValues := func(ctx context.Context, ssmc SSMAPI) error {
		return writeEdgeValues(ctx, ssmc, defaultNamespace, ClassProduction, "fake", map[string]string{"cacheBucket": "ocel-edge-cache"})
	}
	return []adoption{
		{"the cache store", cacheStore(offeredStore()), cacheStore(without(offeredStore(), edge.OfferKeySecretAccessKey))},
		{"the deployments store", deploymentsStore(offeredDeploymentsStore()), deploymentsStore(without(offeredDeploymentsStore(), edge.OfferKeyStoreBootstrapCred))},
		{"the ISR writer", isrWriter(offeredISRWriter("", "cred-prod")), isrWriter(offeredISRWriter("", ""))},
		{"the edge values", edgeValues, edgeValues},
	}
}

func without(offer map[string]string, key string) map[string]string {
	delete(offer, key)
	return offer
}

func TestAdoptionWritesNothingWhenWhatStandsIsWhatItWouldWrite(t *testing.T) {
	for _, tc := range adoptions() {
		t.Run(tc.what, func(t *testing.T) {
			ssmc := newFakeSSM()
			if err := tc.first(context.Background(), ssmc); err != nil {
				t.Fatalf("first adopt: %v", err)
			}
			if ssmc.puts != 1 {
				t.Fatalf("puts = %d, want the first adopt to write once", ssmc.puts)
			}
			if err := tc.first(context.Background(), ssmc); err != nil {
				t.Fatalf("second adopt: %v", err)
			}
			if ssmc.puts != 1 {
				t.Errorf("puts = %d, want the second adopt to write nothing: what stands is what it would write", ssmc.puts)
			}
		})
	}
}

func TestAdoptionWritesNothingWhenAReofferOmitsTheStandingCredential(t *testing.T) {
	for _, tc := range adoptions() {
		t.Run(tc.what, func(t *testing.T) {
			ssmc := newFakeSSM()
			if err := tc.first(context.Background(), ssmc); err != nil {
				t.Fatalf("first adopt: %v", err)
			}
			if err := tc.again(context.Background(), ssmc); err != nil {
				t.Fatalf("re-offer without the credential: %v", err)
			}
			if ssmc.puts != 1 {
				t.Errorf("puts = %d, want the backfilled re-offer to write nothing", ssmc.puts)
			}
		})
	}
}

func TestWriteEdgeValuesRewritesWhatDrifted(t *testing.T) {
	ssmc := newFakeSSM()
	if err := writeEdgeValues(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake", map[string]string{"cacheBucket": "old"}); err != nil {
		t.Fatalf("writeEdgeValues: %v", err)
	}
	if err := writeEdgeValues(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake", map[string]string{"cacheBucket": "new"}); err != nil {
		t.Fatalf("writeEdgeValues (drifted): %v", err)
	}
	if ssmc.puts != 2 {
		t.Errorf("puts = %d, want the drifted values written", ssmc.puts)
	}
	values, err := ReadEdgeValues(context.Background(), ssmc, defaultNamespace, ClassProduction, "fake")
	if err != nil {
		t.Fatalf("ReadEdgeValues: %v", err)
	}
	if values["cacheBucket"] != "new" {
		t.Errorf("stored values = %v, want the ones the edge last handed back", values)
	}
}
