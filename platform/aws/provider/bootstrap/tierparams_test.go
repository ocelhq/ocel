package bootstrap

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/ocelhq/ocel/pkg/environment"
)

type fakeBatchSSM struct {
	params    map[string]string
	calls     int
	requested []string
	decrypted []bool
	err       error
}

func (f *fakeBatchSSM) GetParameters(_ context.Context, in *ssm.GetParametersInput, _ ...func(*ssm.Options)) (*ssm.GetParametersOutput, error) {
	f.calls++
	f.requested = append(f.requested, in.Names...)
	f.decrypted = append(f.decrypted, aws.ToBool(in.WithDecryption))
	if f.err != nil {
		return nil, f.err
	}
	out := &ssm.GetParametersOutput{}
	for _, name := range in.Names {
		v, ok := f.params[name]
		if !ok {
			out.InvalidParameters = append(out.InvalidParameters, name)
			continue
		}
		out.Parameters = append(out.Parameters, ssmtypes.Parameter{Name: aws.String(name), Value: aws.String(v)})
	}
	return out, nil
}

func fullProductionParams() map[string]string {
	names := cloudflareNames(environment.TierProduction)
	return map[string]string{
		passphraseParam:          "pass-1",
		names.credentialsParam:   `{"accessKeyId":"AKIA1","secretAccessKey":"sec-1"}`,
		names.valuesParam:        `{"bucketName":"edge-cache-7f3"}`,
		names.cacheStoreParam:    `{"bucket":"cache-1","endpoint":"https://r2","region":"auto","accessKeyId":"AKIA2","secretAccessKey":"sec-2"}`,
		names.releasesStoreParam: `{"endpoint":"https://store","scriptName":"store","bootstrapCred":"cred"}`,
		names.isrWriterParam:     `{"endpoint":"https://isr","scriptName":"isr","bootstrapCred":"isr-cred"}`,
		names.isrWriterSeedParam: "seed-1",
		names.originSecretParam:  `{"current":"origin-1","createdAt":"2026-01-01T00:00:00Z"}`,
	}
}

func TestReadTierParamsBatches(t *testing.T) {
	ssmc := &fakeBatchSSM{params: fullProductionParams()}

	got, err := ReadTierParams(context.Background(), ssmc, defaultNamespace, environment.TierProduction, KindCloudflare)
	if err != nil {
		t.Fatalf("ReadTierParams: %v", err)
	}
	if ssmc.calls != 1 {
		t.Errorf("GetParameters calls = %d, want 1", ssmc.calls)
	}
	names := cloudflareNames(environment.TierProduction)
	want := []string{
		passphraseParam,
		names.credentialsParam,
		names.valuesParam,
		names.cacheStoreParam,
		names.releasesStoreParam,
		names.isrWriterParam,
		names.isrWriterSeedParam,
		names.originSecretParam,
	}
	slices.Sort(want)
	requested := slices.Clone(ssmc.requested)
	slices.Sort(requested)
	if !slices.Equal(requested, want) {
		t.Errorf("requested names = %v, want %v", requested, want)
	}
	for _, d := range ssmc.decrypted {
		if !d {
			t.Error("GetParameters called without WithDecryption")
		}
	}

	if got.Passphrase != "pass-1" {
		t.Errorf("Passphrase = %q, want pass-1", got.Passphrase)
	}
	if got.EdgeCredentials != (EdgeCredentials{AccessKeyID: "AKIA1", SecretAccessKey: "sec-1"}) {
		t.Errorf("EdgeCredentials = %+v", got.EdgeCredentials)
	}
	if got.EdgeCredentialsErr != nil {
		t.Errorf("EdgeCredentialsErr = %v, want nil", got.EdgeCredentialsErr)
	}
	if !reflect.DeepEqual(got.EdgeValues, map[string]string{"bucketName": "edge-cache-7f3"}) {
		t.Errorf("EdgeValues = %v", got.EdgeValues)
	}
	if got.CacheStore.Bucket != "cache-1" || got.CacheStore.SecretAccessKey != "sec-2" {
		t.Errorf("CacheStore = %+v", got.CacheStore)
	}
	if got.ReleasesStore.ScriptName != "store" {
		t.Errorf("ReleasesStore = %+v", got.ReleasesStore)
	}
	if got.ISRWriter.Endpoint != "https://isr" {
		t.Errorf("ISRWriter = %+v", got.ISRWriter)
	}
	if got.ISRWriterSeed != "seed-1" {
		t.Errorf("ISRWriterSeed = %q, want seed-1", got.ISRWriterSeed)
	}
}

func TestReadTierParamsPreviewNames(t *testing.T) {
	preview := cloudflareNames(environment.TierPreview)
	ssmc := &fakeBatchSSM{params: map[string]string{
		defaultNamespace.PassphraseParamFor(environment.TierPreview): "pass-1",
		preview.credentialsParam:   `{"accessKeyId":"AKIA-prev"}`,
		preview.isrWriterSeedParam: "seed-prev",
	}}

	got, err := ReadTierParams(context.Background(), ssmc, defaultNamespace, environment.TierPreview, KindCloudflare)
	if err != nil {
		t.Fatalf("ReadTierParams(preview): %v", err)
	}
	if got.EdgeCredentials.AccessKeyID != "AKIA-prev" {
		t.Errorf("EdgeCredentials = %+v, want the preview credential", got.EdgeCredentials)
	}
	if got.ISRWriterSeed != "seed-prev" {
		t.Errorf("ISRWriterSeed = %q, want seed-prev", got.ISRWriterSeed)
	}
	for _, name := range ssmc.requested {
		if name == cloudflareNames(environment.TierProduction).credentialsParam {
			t.Errorf("preview read requested production parameter %q", name)
		}
	}
}

func TestReadTierParamsUnknownTier(t *testing.T) {
	ssmc := &fakeBatchSSM{params: fullProductionParams()}
	if _, err := ReadTierParams(context.Background(), ssmc, defaultNamespace, "nonsense", KindCloudflare); err == nil {
		t.Fatal("ReadTierParams(unknown tier) = nil error, want an error")
	}
	if ssmc.calls != 0 {
		t.Errorf("GetParameters calls = %d, want none for an unknown tier", ssmc.calls)
	}
}

func TestReadTierParamsMissingPassphrase(t *testing.T) {
	params := fullProductionParams()
	delete(params, passphraseParam)

	_, err := ReadTierParams(context.Background(), &fakeBatchSSM{params: params}, defaultNamespace, environment.TierProduction, KindCloudflare)
	if err == nil {
		t.Fatal("ReadTierParams without a passphrase = nil error, want an error")
	}
	if !strings.Contains(err.Error(), passphraseParam) {
		t.Errorf("error = %v, want it to name %s", err, passphraseParam)
	}
}

func TestReadTierParamsCallFailure(t *testing.T) {
	ssmc := &fakeBatchSSM{params: fullProductionParams(), err: errors.New("throttled")}
	if _, err := ReadTierParams(context.Background(), ssmc, defaultNamespace, environment.TierProduction, KindCloudflare); err == nil {
		t.Fatal("ReadTierParams with a failing GetParameters = nil error, want an error")
	}
}

func TestReadTierParamsEdgeFailures(t *testing.T) {
	t.Run("absent credentials are reported", func(t *testing.T) {
		params := fullProductionParams()
		delete(params, cloudflareNames(environment.TierProduction).credentialsParam)

		got, err := ReadTierParams(context.Background(), &fakeBatchSSM{params: params}, defaultNamespace, environment.TierProduction, KindCloudflare)
		if err != nil {
			t.Fatalf("ReadTierParams: %v", err)
		}
		if got.EdgeCredentialsErr == nil {
			t.Error("EdgeCredentialsErr = nil, want the absence reported")
		}
		if got.EdgeCredentials != (EdgeCredentials{}) {
			t.Errorf("EdgeCredentials = %+v, want the zero credentials", got.EdgeCredentials)
		}
		if got.Passphrase != "pass-1" {
			t.Errorf("Passphrase = %q, want the rest of the batch intact", got.Passphrase)
		}
	})

	t.Run("unparsable credentials are reported", func(t *testing.T) {
		params := fullProductionParams()
		params[cloudflareNames(environment.TierProduction).credentialsParam] = "{not json"

		got, err := ReadTierParams(context.Background(), &fakeBatchSSM{params: params}, defaultNamespace, environment.TierProduction, KindCloudflare)
		if err != nil {
			t.Fatalf("ReadTierParams: %v", err)
		}
		if got.EdgeCredentialsErr == nil {
			t.Error("EdgeCredentialsErr = nil, want the parse failure reported")
		}
		if got.EdgeCredentials != (EdgeCredentials{}) {
			t.Errorf("EdgeCredentials = %+v, want the zero credentials", got.EdgeCredentials)
		}
	})

	t.Run("absent values are silent", func(t *testing.T) {
		params := fullProductionParams()
		delete(params, cloudflareNames(environment.TierProduction).valuesParam)

		got, err := ReadTierParams(context.Background(), &fakeBatchSSM{params: params}, defaultNamespace, environment.TierProduction, KindCloudflare)
		if err != nil {
			t.Fatalf("ReadTierParams: %v", err)
		}
		if got.EdgeValuesErr != nil {
			t.Errorf("EdgeValuesErr = %v, want an absent values parameter to go unreported", got.EdgeValuesErr)
		}
		if got.EdgeValues != nil {
			t.Errorf("EdgeValues = %v, want none", got.EdgeValues)
		}
	})

	t.Run("unparsable values are reported", func(t *testing.T) {
		params := fullProductionParams()
		params[cloudflareNames(environment.TierProduction).valuesParam] = "{not json"

		got, err := ReadTierParams(context.Background(), &fakeBatchSSM{params: params}, defaultNamespace, environment.TierProduction, KindCloudflare)
		if err != nil {
			t.Fatalf("ReadTierParams: %v", err)
		}
		if got.EdgeValuesErr == nil {
			t.Error("EdgeValuesErr = nil, want the parse failure reported")
		}
		if got.EdgeValues != nil {
			t.Errorf("EdgeValues = %v, want none", got.EdgeValues)
		}
	})
}

func TestReadTierParamsDefersAnUnparsableOriginSecret(t *testing.T) {
	params := fullProductionParams()
	params[cloudflareNames(environment.TierProduction).originSecretParam] = "123f4e5d"

	got, err := ReadTierParams(context.Background(), &fakeBatchSSM{params: params}, defaultNamespace, environment.TierProduction, KindCloudflare)
	if err != nil {
		t.Fatalf("ReadTierParams = %v, want the read to pass the failure along: every call that only takes a bootstrap down reads these and presents no secret", err)
	}
	if got.OriginSecretErr == nil {
		t.Fatal("OriginSecretErr = nil, want the parse failure passed to whoever demands the secret")
	}
	if !strings.Contains(got.OriginSecretErr.Error(), cloudflareNames(environment.TierProduction).originSecretParam) {
		t.Errorf("OriginSecretErr = %v, want it to name the parameter", got.OriginSecretErr)
	}
	if got.OriginSecret.Present() {
		t.Errorf("OriginSecret = %+v, want none present", got.OriginSecret)
	}
	if got.Passphrase != "pass-1" {
		t.Errorf("Passphrase = %q, want the rest of the batch intact", got.Passphrase)
	}
}

func TestReadCoreParamsDefersAnUnparsableOriginSecret(t *testing.T) {
	ssmc := &fakeBatchSSM{params: map[string]string{
		passphraseParam:   "pass-1",
		originSecretParam: "123f4e5d",
	}}

	got, err := ReadCoreParams(context.Background(), ssmc, defaultNamespace, environment.TierProduction)
	if err != nil {
		t.Fatalf("ReadCoreParams = %v, want the read to pass the failure along rather than refuse", err)
	}
	if got.OriginSecretErr == nil {
		t.Fatal("OriginSecretErr = nil, want the parse failure passed to whoever demands the secret")
	}
}

func TestReadTierParamsAbsentOptional(t *testing.T) {
	ssmc := &fakeBatchSSM{params: map[string]string{passphraseParam: "pass-1"}}

	got, err := ReadTierParams(context.Background(), ssmc, defaultNamespace, environment.TierProduction, KindCloudflare)
	if err != nil {
		t.Fatalf("ReadTierParams: %v", err)
	}
	if got.CacheStore != (CacheStore{}) {
		t.Errorf("CacheStore = %+v, want the zero store", got.CacheStore)
	}
	if got.ReleasesStore != (ReleasesStore{}) {
		t.Errorf("ReleasesStore = %+v, want the zero store", got.ReleasesStore)
	}
	if got.ISRWriter != (ISRWriter{}) {
		t.Errorf("ISRWriter = %+v, want the zero writer", got.ISRWriter)
	}
	if got.ISRWriterSeed != "" {
		t.Errorf("ISRWriterSeed = %q, want empty", got.ISRWriterSeed)
	}
}

func TestReadTierParamsUnparsableStores(t *testing.T) {
	names := cloudflareNames(environment.TierProduction)
	for _, name := range []string{
		names.cacheStoreParam,
		names.releasesStoreParam,
		names.isrWriterParam,
	} {
		t.Run(name, func(t *testing.T) {
			params := fullProductionParams()
			params[name] = "{not json"

			if _, err := ReadTierParams(context.Background(), &fakeBatchSSM{params: params}, defaultNamespace, environment.TierProduction, KindCloudflare); err == nil {
				t.Fatalf("ReadTierParams with an unparsable %s = nil error, want an error", name)
			}
		})
	}
}

func TestGetParametersChunks(t *testing.T) {
	ssmc := &fakeBatchSSM{params: map[string]string{"a": "1", "w": "23"}}
	names := make([]string, 0, 23)
	for i := range 23 {
		names = append(names, string(rune('a'+i)))
	}

	got, err := getParameters(context.Background(), ssmc, names)
	if err != nil {
		t.Fatalf("getParameters: %v", err)
	}
	if ssmc.calls != 3 {
		t.Errorf("GetParameters calls = %d, want 3 for 23 names", ssmc.calls)
	}
	if len(ssmc.requested) != 23 {
		t.Errorf("requested %d names, want 23", len(ssmc.requested))
	}
	if !reflect.DeepEqual(got, map[string]string{"a": "1", "w": "23"}) {
		t.Errorf("getParameters = %v, want only the present names", got)
	}
}
