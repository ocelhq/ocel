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
	"github.com/ocelhq/ocel/pkg/refusal"
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
		names.releasesStoreParam: `{"endpoint":"https://store","scriptName":"store","bootstrapCred":"cred"}`,
		names.purgeSeedParam:     "seed-1",
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
		names.releasesStoreParam,
		names.purgeSeedParam,
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
	if got.ReleasesStore.ScriptName != "store" {
		t.Errorf("ReleasesStore = %+v", got.ReleasesStore)
	}
	if got.PurgeSeed != "seed-1" {
		t.Errorf("PurgeSeed = %q, want seed-1", got.PurgeSeed)
	}
}

func TestReadTierParamsPreviewNames(t *testing.T) {
	preview := cloudflareNames(environment.TierPreview)
	ssmc := &fakeBatchSSM{params: map[string]string{
		defaultNamespace.PassphraseParamFor(environment.TierPreview): "pass-1",
		preview.credentialsParam: `{"accessKeyId":"AKIA-prev"}`,
		preview.purgeSeedParam:   "seed-prev",
	}}

	got, err := ReadTierParams(context.Background(), ssmc, defaultNamespace, environment.TierPreview, KindCloudflare)
	if err != nil {
		t.Fatalf("ReadTierParams(preview): %v", err)
	}
	if got.EdgeCredentials.AccessKeyID != "AKIA-prev" {
		t.Errorf("EdgeCredentials = %+v, want the preview credential", got.EdgeCredentials)
	}
	if got.PurgeSeed != "seed-prev" {
		t.Errorf("PurgeSeed = %q, want seed-prev", got.PurgeSeed)
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

func TestReadingATierWithoutItsPassphraseRefusesUntilThatTierIsBootstrapped(t *testing.T) {
	for _, tier := range bothTiers {
		param := defaultNamespace.PassphraseParamFor(tier)
		readers := map[string]func(SSMBatchAPI) error{
			"ReadTierParams": func(api SSMBatchAPI) error {
				_, err := ReadTierParams(context.Background(), api, defaultNamespace, tier, KindCloudflare)
				return err
			},
			"ReadCoreParams": func(api SSMBatchAPI) error {
				_, err := ReadCoreParams(context.Background(), api, defaultNamespace, tier)
				return err
			},
		}
		for name, read := range readers {
			err := read(&fakeBatchSSM{params: map[string]string{}})
			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
				t.Errorf("%s for the %s tier without %s = %v, want a %s refusal", name, tier, param, err, refusal.CodeNotReady)
				continue
			}
			for _, want := range []string{param, "`ocel bootstrap " + string(tier) + "`"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%s for the %s tier refused with %q, want it to name %s", name, tier, err, want)
				}
			}
		}
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
		if !errors.Is(got.EdgeCredentialsErr, ErrEdgeCredentialsAbsent) {
			t.Errorf("EdgeCredentialsErr = %v, want the absence reported", got.EdgeCredentialsErr)
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
		if got.EdgeCredentialsErr == nil || errors.Is(got.EdgeCredentialsErr, ErrEdgeCredentialsAbsent) {
			t.Errorf("EdgeCredentialsErr = %v, want the parse failure reported as something other than absence", got.EdgeCredentialsErr)
		}
		if got.EdgeCredentials != (EdgeCredentials{}) {
			t.Errorf("EdgeCredentials = %+v, want the zero credentials", got.EdgeCredentials)
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
	if got.ReleasesStore != (ReleasesStore{}) {
		t.Errorf("ReleasesStore = %+v, want the zero store", got.ReleasesStore)
	}
	if got.PurgeSeed != "" {
		t.Errorf("PurgeSeed = %q, want empty", got.PurgeSeed)
	}
}

func TestReadTierParamsUnparsableStores(t *testing.T) {
	names := cloudflareNames(environment.TierProduction)
	for _, name := range []string{
		names.releasesStoreParam,
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
