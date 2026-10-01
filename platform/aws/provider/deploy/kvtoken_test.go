package deploy

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/ocelhq/ocel/pkg/provider"
)

type fakeParameters struct {
	mu       sync.Mutex
	values   map[string]string
	types    map[string]ssmtypes.ParameterType
	puts     int
	listings int
	deleted  []string

	beforePut func(name string)
}

func newFakeParameters() *fakeParameters {
	return &fakeParameters{values: map[string]string{}, types: map[string]ssmtypes.ParameterType{}}
}

func (f *fakeParameters) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, found := f.values[aws.ToString(in.Name)]
	if !found {
		return nil, &ssmtypes.ParameterNotFound{}
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Name: in.Name, Value: aws.String(value)}}, nil
}

func (f *fakeParameters) PutParameter(_ context.Context, in *ssm.PutParameterInput, _ ...func(*ssm.Options)) (*ssm.PutParameterOutput, error) {
	name := aws.ToString(in.Name)
	if f.beforePut != nil {
		f.beforePut(name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	if _, exists := f.values[name]; exists && !aws.ToBool(in.Overwrite) {
		return nil, &ssmtypes.ParameterAlreadyExists{}
	}
	f.values[name] = aws.ToString(in.Value)
	f.types[name] = in.Type
	return &ssm.PutParameterOutput{}, nil
}

func (f *fakeParameters) DeleteParameter(_ context.Context, in *ssm.DeleteParameterInput, _ ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.Name)
	if _, found := f.values[name]; !found {
		return nil, &ssmtypes.ParameterNotFound{}
	}
	delete(f.values, name)
	f.deleted = append(f.deleted, name)
	return &ssm.DeleteParameterOutput{}, nil
}

func (f *fakeParameters) GetParametersByPath(_ context.Context, in *ssm.GetParametersByPathInput, _ ...func(*ssm.Options)) (*ssm.GetParametersByPathOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listings++
	path := strings.TrimSuffix(aws.ToString(in.Path), "/") + "/"
	var names []string
	for name := range f.values {
		if rest, under := strings.CutPrefix(name, path); under && rest != "" && !strings.Contains(rest, "/") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	page := int(aws.ToInt32(in.MaxResults))
	if page == 0 || page > 10 {
		page = 10
	}
	start := 0
	if in.NextToken != nil {
		start, _ = strconv.Atoi(*in.NextToken)
	}
	end := min(start+page, len(names))
	out := &ssm.GetParametersByPathOutput{}
	for _, name := range names[start:end] {
		out.Parameters = append(out.Parameters, ssmtypes.Parameter{Name: aws.String(name)})
	}
	if end < len(names) {
		out.NextToken = aws.String(strconv.Itoa(end))
	}
	return out, nil
}

var mintedToken = regexp.MustCompile(`^[A-Za-z0-9]{64}$`)

func TestAStoreWithNoTokenMintsOneOfSixtyFourLettersAndDigitsKeptAsASecureString(t *testing.T) {
	t.Parallel()

	params := newFakeParameters()
	token, err := ensureKVToken(t.Context(), params, "/ocel/kv/shop/prod/kv--cache")
	if err != nil {
		t.Fatalf("ensureKVToken() = %v", err)
	}
	if !mintedToken.MatchString(token) {
		t.Errorf("ensureKVToken() = %q, want 64 letters and digits", token)
	}
	if params.values["/ocel/kv/shop/prod/kv--cache"] != token || params.types["/ocel/kv/shop/prod/kv--cache"] != ssmtypes.ParameterTypeSecureString {
		t.Errorf("the parameter holds %q as %s, want the token as a SecureString", params.values["/ocel/kv/shop/prod/kv--cache"], params.types["/ocel/kv/shop/prod/kv--cache"])
	}

	other, err := ensureKVToken(t.Context(), params, "/ocel/kv/shop/prod/kv--sessions")
	if err != nil {
		t.Fatalf("ensureKVToken() = %v", err)
	}
	if other == token {
		t.Error("two stores were minted the same token")
	}
}

func TestAStoreWithATokenKeepsIt(t *testing.T) {
	t.Parallel()

	params := newFakeParameters()
	params.values["/ocel/kv/shop/prod/kv--cache"] = "the-token-it-already-has"

	token, err := ensureKVToken(t.Context(), params, "/ocel/kv/shop/prod/kv--cache")
	if err != nil {
		t.Fatalf("ensureKVToken() = %v", err)
	}
	if token != "the-token-it-already-has" || params.puts != 0 {
		t.Errorf("ensureKVToken() = %q after %d writes, want the token kept and nothing written", token, params.puts)
	}
}

func TestATokenAConcurrentDeployMintedFirstIsTheOneBothUse(t *testing.T) {
	t.Parallel()

	params := newFakeParameters()
	params.beforePut = func(name string) {
		params.mu.Lock()
		defer params.mu.Unlock()
		if _, taken := params.values[name]; !taken {
			params.values[name] = "the-other-deploys-token"
		}
	}

	token, err := ensureKVToken(t.Context(), params, "/ocel/kv/shop/prod/kv--cache")
	if err != nil {
		t.Fatalf("ensureKVToken() = %v", err)
	}
	if token != "the-other-deploys-token" {
		t.Errorf("ensureKVToken() = %q, want the token the deploy that wrote first minted", token)
	}
}

func TestAPreviewReadsATokenAndWritesNone(t *testing.T) {
	t.Parallel()

	params := newFakeParameters()
	token, err := previewKVToken(t.Context(), params, "/ocel/kv/shop/prod/kv--cache")
	if err != nil {
		t.Fatalf("previewKVToken() = %v", err)
	}
	if token == "" || params.puts != 0 {
		t.Errorf("previewKVToken() = %q after %d writes, want a stand-in token and nothing written", token, params.puts)
	}

	params.values["/ocel/kv/shop/prod/kv--cache"] = "the-token-it-has"
	if token, _ := previewKVToken(t.Context(), params, "/ocel/kv/shop/prod/kv--cache"); token != "the-token-it-has" {
		t.Errorf("previewKVToken() = %q, want the token the store already has, so the preview shows no change to it", token)
	}
}

func TestAStoresTokenParameterIsNamedForItsProjectEnvironmentAndName(t *testing.T) {
	t.Parallel()

	if got := kvTokenParameter("/ocel/kv", "shop", "prod", "kv--cache"); got != "/ocel/kv/shop/prod/kv--cache" {
		t.Errorf("kvTokenParameter() = %q, want /ocel/kv/shop/prod/kv--cache", got)
	}
}

func TestAKVBindingCarriesTheTokenItsParameterHoldsOverTLS(t *testing.T) {
	t.Parallel()

	params := newFakeParameters()
	params.values["/ocel/kv/shop/prod/kv--cache"] = "the-token"

	binding, err := collectKVBinding(t.Context(), params, "kv--cache", map[string]any{
		outputKeyHost:               "master.ocel-app-shop-prod-cache.abc123.use1.cache.amazonaws.com",
		outputKeyPort:               float64(6379),
		outputKeyAuthTokenParameter: "/ocel/kv/shop/prod/kv--cache",
	})
	if err != nil {
		t.Fatalf("collectKVBinding() = %v", err)
	}
	got := bindingOf(provider.BindingKV, binding)
	want := map[string]string{
		provider.PropertyHost:     "master.ocel-app-shop-prod-cache.abc123.use1.cache.amazonaws.com",
		provider.PropertyPort:     "6379",
		provider.PropertyUsername: "",
		provider.PropertyPassword: "the-token",
		provider.PropertyTLS:      "true",
	}
	for key, value := range want {
		if got.Properties[key] != value {
			t.Errorf("binding %s = %q, want %q", key, got.Properties[key], value)
		}
	}
	if err := provider.VerifyProperties(got); err != nil {
		t.Errorf("the kv binding is one providerserver refuses to record: %v", err)
	}
}

func TestAKVBindingWhoseTokenIsGoneIsRefusedNamingTheStore(t *testing.T) {
	t.Parallel()

	_, err := collectKVBinding(t.Context(), newFakeParameters(), "kv--cache", map[string]any{
		outputKeyHost:               "master.example",
		outputKeyPort:               float64(6379),
		outputKeyAuthTokenParameter: "/ocel/kv/shop/prod/kv--cache",
	})
	if err == nil || !strings.Contains(err.Error(), "kv--cache") {
		t.Errorf("collectKVBinding() = %v, want it refused naming the store", err)
	}
}

func TestRemovingAStoreDeletesItsTokenAndAGoneTokenIsNoError(t *testing.T) {
	t.Parallel()

	params := newFakeParameters()
	params.values["/ocel/kv/shop/prod/kv--cache"] = "the-token"

	if err := deleteKVTokens(t.Context(), params, []string{"/ocel/kv/shop/prod/kv--cache", "/ocel/kv/shop/prod/kv--gone"}); err != nil {
		t.Fatalf("deleteKVTokens() = %v", err)
	}
	if _, kept := params.values["/ocel/kv/shop/prod/kv--cache"]; kept {
		t.Error("deleteKVTokens() left the token behind")
	}
}
