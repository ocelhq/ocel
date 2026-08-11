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
	return map[string]string{
		PassphraseParamName:                  "pass-1",
		EdgeCredentialsParamName:             `{"accessKeyId":"AKIA1","secretAccessKey":"sec-1"}`,
		EdgeValuesParamName:                  `{"bucketName":"edge-cache-7f3"}`,
		CacheStoreParamName:                  `{"bucket":"cache-1","endpoint":"https://r2","region":"auto","accessKeyId":"AKIA2","secretAccessKey":"sec-2"}`,
		DeploymentsStoreParamName:            `{"endpoint":"https://store","scriptName":"store","bootstrapCred":"cred"}`,
		ISRWriterParamName:                   `{"endpoint":"https://isr","scriptName":"isr","bootstrapCred":"isr-cred"}`,
		ISRWriterSeedParamName:               "seed-1",
		RootStackStateParamPrefix + "proj-1": `{"zone":"z1"}`,
	}
}

func TestReadClassParamsBatchesOneCall(t *testing.T) {
	ssmc := &fakeBatchSSM{params: fullProductionParams()}

	got, err := ReadClassParams(context.Background(), ssmc, ClassProduction, "proj-1")
	if err != nil {
		t.Fatalf("ReadClassParams: %v", err)
	}
	if ssmc.calls != 1 {
		t.Errorf("GetParameters calls = %d, want 1", ssmc.calls)
	}
	want := []string{
		PassphraseParamName,
		EdgeCredentialsParamName,
		EdgeValuesParamName,
		CacheStoreParamName,
		DeploymentsStoreParamName,
		ISRWriterParamName,
		ISRWriterSeedParamName,
		RootStackStateParamPrefix + "proj-1",
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
	if !reflect.DeepEqual(map[string]string(got.RootStackState), map[string]string{"zone": "z1"}) {
		t.Errorf("RootStackState = %v", got.RootStackState)
	}
}

func TestReadClassParamsPreviewNames(t *testing.T) {
	ssmc := &fakeBatchSSM{params: map[string]string{
		PassphraseParamName:                        "pass-1",
		EdgeCredentialsPreviewParamName:            `{"accessKeyId":"AKIA-prev"}`,
		ISRWriterSeedPreviewParamName:              "seed-prev",
		PreviewRootStackStateParamPrefix + "proj1": `{"zone":"zp"}`,
	}}

	got, err := ReadClassParams(context.Background(), ssmc, ClassPreview, "proj1")
	if err != nil {
		t.Fatalf("ReadClassParams(preview): %v", err)
	}
	if got.EdgeCredentials.AccessKeyID != "AKIA-prev" {
		t.Errorf("EdgeCredentials = %+v, want the preview credential", got.EdgeCredentials)
	}
	if got.ISRWriterSeed != "seed-prev" {
		t.Errorf("ISRWriterSeed = %q, want seed-prev", got.ISRWriterSeed)
	}
	if !reflect.DeepEqual(map[string]string(got.RootStackState), map[string]string{"zone": "zp"}) {
		t.Errorf("RootStackState = %v, want the preview state", got.RootStackState)
	}
	for _, name := range ssmc.requested {
		if name == EdgeCredentialsParamName || name == RootStackStateParamPrefix+"proj1" {
			t.Errorf("preview read requested production parameter %q", name)
		}
	}
}

func TestReadClassParamsUnknownClass(t *testing.T) {
	ssmc := &fakeBatchSSM{params: fullProductionParams()}
	if _, err := ReadClassParams(context.Background(), ssmc, "nonsense", "proj-1"); err == nil {
		t.Fatal("ReadClassParams(unknown class) = nil error, want an error")
	}
	if ssmc.calls != 0 {
		t.Errorf("GetParameters calls = %d, want none for an unknown class", ssmc.calls)
	}
}

func TestReadClassParamsMissingPassphraseIsFatal(t *testing.T) {
	params := fullProductionParams()
	delete(params, PassphraseParamName)

	_, err := ReadClassParams(context.Background(), &fakeBatchSSM{params: params}, ClassProduction, "proj-1")
	if err == nil {
		t.Fatal("ReadClassParams without a passphrase = nil error, want an error")
	}
	if !strings.Contains(err.Error(), PassphraseParamName) {
		t.Errorf("error = %v, want it to name %s", err, PassphraseParamName)
	}
}

func TestReadClassParamsCallFailureIsFatal(t *testing.T) {
	ssmc := &fakeBatchSSM{params: fullProductionParams(), err: errors.New("throttled")}
	if _, err := ReadClassParams(context.Background(), ssmc, ClassProduction, "proj-1"); err == nil {
		t.Fatal("ReadClassParams with a failing GetParameters = nil error, want an error")
	}
}

func TestReadClassParamsToleratedEdgeFailures(t *testing.T) {
	t.Run("absent credentials are reported, not fatal", func(t *testing.T) {
		params := fullProductionParams()
		delete(params, EdgeCredentialsParamName)

		got, err := ReadClassParams(context.Background(), &fakeBatchSSM{params: params}, ClassProduction, "proj-1")
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

	t.Run("unparsable credentials are reported, not fatal", func(t *testing.T) {
		params := fullProductionParams()
		params[EdgeCredentialsParamName] = "{not json"

		got, err := ReadClassParams(context.Background(), &fakeBatchSSM{params: params}, ClassProduction, "proj-1")
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
		delete(params, EdgeValuesParamName)

		got, err := ReadClassParams(context.Background(), &fakeBatchSSM{params: params}, ClassProduction, "proj-1")
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

	t.Run("unparsable values are reported, not fatal", func(t *testing.T) {
		params := fullProductionParams()
		params[EdgeValuesParamName] = "{not json"

		got, err := ReadClassParams(context.Background(), &fakeBatchSSM{params: params}, ClassProduction, "proj-1")
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

func TestReadClassParamsAbsentOptionalParams(t *testing.T) {
	ssmc := &fakeBatchSSM{params: map[string]string{PassphraseParamName: "pass-1"}}

	got, err := ReadClassParams(context.Background(), ssmc, ClassProduction, "proj-1")
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
	if got.RootStackState != nil {
		t.Errorf("RootStackState = %v, want nil", got.RootStackState)
	}
}

func TestReadClassParamsUnparsableStoresAreFatal(t *testing.T) {
	for _, name := range []string{
		CacheStoreParamName,
		DeploymentsStoreParamName,
		ISRWriterParamName,
		RootStackStateParamPrefix + "proj-1",
	} {
		t.Run(name, func(t *testing.T) {
			params := fullProductionParams()
			params[name] = "{not json"

			if _, err := ReadClassParams(context.Background(), &fakeBatchSSM{params: params}, ClassProduction, "proj-1"); err == nil {
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
