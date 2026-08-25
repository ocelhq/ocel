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
	names := cloudflareNames(ClassProduction)
	return map[string]string{
		PassphraseParamName:         "pass-1",
		names.credentialsParam:      `{"accessKeyId":"AKIA1","secretAccessKey":"sec-1"}`,
		names.valuesParam:           `{"bucketName":"edge-cache-7f3"}`,
		names.cacheStoreParam:       `{"bucket":"cache-1","endpoint":"https://r2","region":"auto","accessKeyId":"AKIA2","secretAccessKey":"sec-2"}`,
		names.deploymentsStoreParam: `{"endpoint":"https://store","scriptName":"store","bootstrapCred":"cred"}`,
		names.isrWriterParam:        `{"endpoint":"https://isr","scriptName":"isr","bootstrapCred":"isr-cred"}`,
		names.isrWriterSeedParam:    "seed-1",
		names.originSecretParam:     "origin-1",
	}
}

func TestReadClassParamsBatches(t *testing.T) {
	ssmc := &fakeBatchSSM{params: fullProductionParams()}

	got, err := ReadClassParams(context.Background(), ssmc, ClassProduction, KindCloudflare)
	if err != nil {
		t.Fatalf("ReadClassParams: %v", err)
	}
	if ssmc.calls != 1 {
		t.Errorf("GetParameters calls = %d, want 1", ssmc.calls)
	}
	names := cloudflareNames(ClassProduction)
	want := []string{
		PassphraseParamName,
		names.credentialsParam,
		names.valuesParam,
		names.cacheStoreParam,
		names.deploymentsStoreParam,
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
	if got.DeploymentsStore.ScriptName != "store" {
		t.Errorf("DeploymentsStore = %+v", got.DeploymentsStore)
	}
	if got.ISRWriter.Endpoint != "https://isr" {
		t.Errorf("ISRWriter = %+v", got.ISRWriter)
	}
	if got.ISRWriterSeed != "seed-1" {
		t.Errorf("ISRWriterSeed = %q, want seed-1", got.ISRWriterSeed)
	}
}

func TestReadClassParamsPreviewNames(t *testing.T) {
	preview := cloudflareNames(ClassPreview)
	ssmc := &fakeBatchSSM{params: map[string]string{
		PassphraseParamName:        "pass-1",
		preview.credentialsParam:   `{"accessKeyId":"AKIA-prev"}`,
		preview.isrWriterSeedParam: "seed-prev",
	}}

	got, err := ReadClassParams(context.Background(), ssmc, ClassPreview, KindCloudflare)
	if err != nil {
		t.Fatalf("ReadClassParams(preview): %v", err)
	}
	if got.EdgeCredentials.AccessKeyID != "AKIA-prev" {
		t.Errorf("EdgeCredentials = %+v, want the preview credential", got.EdgeCredentials)
	}
	if got.ISRWriterSeed != "seed-prev" {
		t.Errorf("ISRWriterSeed = %q, want seed-prev", got.ISRWriterSeed)
	}
	for _, name := range ssmc.requested {
		if name == cloudflareNames(ClassProduction).credentialsParam {
			t.Errorf("preview read requested production parameter %q", name)
		}
	}
}

func TestReadClassParamsUnknownClass(t *testing.T) {
	ssmc := &fakeBatchSSM{params: fullProductionParams()}
	if _, err := ReadClassParams(context.Background(), ssmc, "nonsense", KindCloudflare); err == nil {
		t.Fatal("ReadClassParams(unknown class) = nil error, want an error")
	}
	if ssmc.calls != 0 {
		t.Errorf("GetParameters calls = %d, want none for an unknown class", ssmc.calls)
	}
}

func TestReadClassParamsMissingPassphrase(t *testing.T) {
	params := fullProductionParams()
	delete(params, PassphraseParamName)

	_, err := ReadClassParams(context.Background(), &fakeBatchSSM{params: params}, ClassProduction, KindCloudflare)
	if err == nil {
		t.Fatal("ReadClassParams without a passphrase = nil error, want an error")
	}
	if !strings.Contains(err.Error(), PassphraseParamName) {
		t.Errorf("error = %v, want it to name %s", err, PassphraseParamName)
	}
}

func TestReadClassParamsCallFailure(t *testing.T) {
	ssmc := &fakeBatchSSM{params: fullProductionParams(), err: errors.New("throttled")}
	if _, err := ReadClassParams(context.Background(), ssmc, ClassProduction, KindCloudflare); err == nil {
		t.Fatal("ReadClassParams with a failing GetParameters = nil error, want an error")
	}
}

func TestReadClassParamsEdgeFailures(t *testing.T) {
	t.Run("absent credentials are reported", func(t *testing.T) {
		params := fullProductionParams()
		delete(params, cloudflareNames(ClassProduction).credentialsParam)

		got, err := ReadClassParams(context.Background(), &fakeBatchSSM{params: params}, ClassProduction, KindCloudflare)
		if err != nil {
			t.Fatalf("ReadClassParams: %v", err)
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
		params[cloudflareNames(ClassProduction).credentialsParam] = "{not json"

		got, err := ReadClassParams(context.Background(), &fakeBatchSSM{params: params}, ClassProduction, KindCloudflare)
		if err != nil {
			t.Fatalf("ReadClassParams: %v", err)
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
		delete(params, cloudflareNames(ClassProduction).valuesParam)

		got, err := ReadClassParams(context.Background(), &fakeBatchSSM{params: params}, ClassProduction, KindCloudflare)
		if err != nil {
			t.Fatalf("ReadClassParams: %v", err)
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
		params[cloudflareNames(ClassProduction).valuesParam] = "{not json"

		got, err := ReadClassParams(context.Background(), &fakeBatchSSM{params: params}, ClassProduction, KindCloudflare)
		if err != nil {
			t.Fatalf("ReadClassParams: %v", err)
		}
		if got.EdgeValuesErr == nil {
			t.Error("EdgeValuesErr = nil, want the parse failure reported")
		}
		if got.EdgeValues != nil {
			t.Errorf("EdgeValues = %v, want none", got.EdgeValues)
		}
	})
}

func TestReadClassParamsAbsentOptional(t *testing.T) {
	ssmc := &fakeBatchSSM{params: map[string]string{PassphraseParamName: "pass-1"}}

	got, err := ReadClassParams(context.Background(), ssmc, ClassProduction, KindCloudflare)
	if err != nil {
		t.Fatalf("ReadClassParams: %v", err)
	}
	if got.CacheStore != (CacheStore{}) {
		t.Errorf("CacheStore = %+v, want the zero store", got.CacheStore)
	}
	if got.DeploymentsStore != (DeploymentsStore{}) {
		t.Errorf("DeploymentsStore = %+v, want the zero store", got.DeploymentsStore)
	}
	if got.ISRWriter != (ISRWriter{}) {
		t.Errorf("ISRWriter = %+v, want the zero writer", got.ISRWriter)
	}
	if got.ISRWriterSeed != "" {
		t.Errorf("ISRWriterSeed = %q, want empty", got.ISRWriterSeed)
	}
}

func TestReadClassParamsUnparsableStores(t *testing.T) {
	names := cloudflareNames(ClassProduction)
	for _, name := range []string{
		names.cacheStoreParam,
		names.deploymentsStoreParam,
		names.isrWriterParam,
	} {
		t.Run(name, func(t *testing.T) {
			params := fullProductionParams()
			params[name] = "{not json"

			if _, err := ReadClassParams(context.Background(), &fakeBatchSSM{params: params}, ClassProduction, KindCloudflare); err == nil {
				t.Fatalf("ReadClassParams with an unparsable %s = nil error, want an error", name)
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

func teardownProductionParams() map[string]string {
	names := cloudflareNames(ClassProduction)
	return map[string]string{
		PassphraseParamName:   "pass-1",
		names.cacheStoreParam: `{"bucket":"cache-1","secretAccessKey":"sec-2"}`,
		names.isrWriterParam:  `{"endpoint":"https://isr","bootstrapCred":"isr-cred"}`,
	}
}

func TestReadTeardownParamsBatches(t *testing.T) {
	ssmc := &fakeBatchSSM{params: teardownProductionParams()}

	got, err := ReadTeardownParams(context.Background(), ssmc, ClassProduction, KindCloudflare)
	if err != nil {
		t.Fatalf("ReadTeardownParams: %v", err)
	}
	if ssmc.calls != 1 {
		t.Errorf("GetParameters calls = %d, want 1", ssmc.calls)
	}
	names := cloudflareNames(ClassProduction)
	want := []string{
		PassphraseParamName,
		names.cacheStoreParam,
		names.isrWriterParam,
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

	if got.Passphrase != "pass-1" || got.PassphraseErr != nil {
		t.Errorf("Passphrase = %q (err %v), want pass-1", got.Passphrase, got.PassphraseErr)
	}
	if got.CacheStore.Bucket != "cache-1" || got.CacheStore.SecretAccessKey != "sec-2" {
		t.Errorf("CacheStore = %+v", got.CacheStore)
	}
	if got.ISRWriter.Endpoint != "https://isr" || got.ISRWriter.BootstrapCred != "isr-cred" {
		t.Errorf("ISRWriter = %+v", got.ISRWriter)
	}
}

func TestReadTeardownParamsPreviewNames(t *testing.T) {
	ssmc := &fakeBatchSSM{params: map[string]string{
		PassphraseParamName:                           "pass-1",
		cloudflareNames(ClassPreview).cacheStoreParam: `{"bucket":"cache-prev"}`,
	}}

	got, err := ReadTeardownParams(context.Background(), ssmc, ClassPreview, KindCloudflare)
	if err != nil {
		t.Fatalf("ReadTeardownParams(preview): %v", err)
	}
	if got.CacheStore.Bucket != "cache-prev" {
		t.Errorf("CacheStore = %+v, want the preview store", got.CacheStore)
	}
	for _, name := range ssmc.requested {
		if name == cloudflareNames(ClassProduction).cacheStoreParam {
			t.Errorf("preview read requested production parameter %q", name)
		}
	}
}

func TestReadTeardownParamsUnknownClass(t *testing.T) {
	ssmc := &fakeBatchSSM{params: teardownProductionParams()}
	if _, err := ReadTeardownParams(context.Background(), ssmc, "nonsense", KindCloudflare); err == nil {
		t.Fatal("ReadTeardownParams(unknown class) = nil error, want an error")
	}
	if ssmc.calls != 0 {
		t.Errorf("GetParameters calls = %d, want none for an unknown class", ssmc.calls)
	}
}

func TestReadTeardownParamsCallFailure(t *testing.T) {
	ssmc := &fakeBatchSSM{params: teardownProductionParams(), err: errors.New("throttled")}
	if _, err := ReadTeardownParams(context.Background(), ssmc, ClassProduction, KindCloudflare); err == nil {
		t.Fatal("ReadTeardownParams with a failing GetParameters = nil error, want an error")
	}
}

func TestReadTeardownParamsMissingPassphrase(t *testing.T) {
	params := teardownProductionParams()
	delete(params, PassphraseParamName)

	got, err := ReadTeardownParams(context.Background(), &fakeBatchSSM{params: params}, ClassProduction, KindCloudflare)
	if err != nil {
		t.Fatalf("ReadTeardownParams: %v", err)
	}
	if got.PassphraseErr == nil {
		t.Fatal("PassphraseErr = nil, want an absent passphrase reported as fatal to whoever needs it")
	}
	if !strings.Contains(got.PassphraseErr.Error(), PassphraseParamName) {
		t.Errorf("PassphraseErr = %v, want it to name %s", got.PassphraseErr, PassphraseParamName)
	}
	if got.CacheStore.Bucket != "cache-1" {
		t.Errorf("CacheStore = %+v, want the rest of the batch intact", got.CacheStore)
	}
}

func TestReadTeardownParamsAbsentOptional(t *testing.T) {
	ssmc := &fakeBatchSSM{params: map[string]string{PassphraseParamName: "pass-1"}}

	got, err := ReadTeardownParams(context.Background(), ssmc, ClassProduction, KindCloudflare)
	if err != nil {
		t.Fatalf("ReadTeardownParams: %v", err)
	}
	if got.CacheStore != (CacheStore{}) {
		t.Errorf("CacheStore = %+v, want the zero store", got.CacheStore)
	}
	if got.ISRWriter != (ISRWriter{}) {
		t.Errorf("ISRWriter = %+v, want the zero writer", got.ISRWriter)
	}
}

func TestReadTeardownParamsUnparsable(t *testing.T) {
	t.Run("a cache store falls back to the zero store", func(t *testing.T) {
		params := teardownProductionParams()
		params[cloudflareNames(ClassProduction).cacheStoreParam] = "{not json"

		got, err := ReadTeardownParams(context.Background(), &fakeBatchSSM{params: params}, ClassProduction, KindCloudflare)
		if err != nil {
			t.Fatalf("ReadTeardownParams: %v", err)
		}
		if got.CacheStore != (CacheStore{}) {
			t.Errorf("CacheStore = %+v, want the zero store", got.CacheStore)
		}
	})

	t.Run("an isr writer falls back to the zero writer", func(t *testing.T) {
		params := teardownProductionParams()
		params[cloudflareNames(ClassProduction).isrWriterParam] = "{not json"

		got, err := ReadTeardownParams(context.Background(), &fakeBatchSSM{params: params}, ClassProduction, KindCloudflare)
		if err != nil {
			t.Fatalf("ReadTeardownParams: %v", err)
		}
		if got.ISRWriter != (ISRWriter{}) {
			t.Errorf("ISRWriter = %+v, want the zero writer", got.ISRWriter)
		}
	})

}
