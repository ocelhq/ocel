package deploy

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"

	"github.com/ocelhq/ocel/pkg/provider"
)

type fakeSigningKeys struct {
	mu       sync.Mutex
	values   map[string]string
	tags     map[string][]smtypes.Tag
	creates  int
	listings int
	deleted  []string
	forced   []bool

	beforeCreate func(name string)
}

func newFakeSigningKeys() *fakeSigningKeys {
	return &fakeSigningKeys{values: map[string]string{}, tags: map[string][]smtypes.Tag{}}
}

func (f *fakeSigningKeys) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, found := f.values[aws.ToString(in.SecretId)]
	if !found {
		return nil, &smtypes.ResourceNotFoundException{}
	}
	return &secretsmanager.GetSecretValueOutput{Name: in.SecretId, SecretString: aws.String(value)}, nil
}

func (f *fakeSigningKeys) CreateSecret(_ context.Context, in *secretsmanager.CreateSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
	name := aws.ToString(in.Name)
	if f.beforeCreate != nil {
		f.beforeCreate(name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	if _, exists := f.values[name]; exists {
		return nil, &smtypes.ResourceExistsException{}
	}
	f.values[name] = aws.ToString(in.SecretString)
	f.tags[name] = in.Tags
	return &secretsmanager.CreateSecretOutput{Name: in.Name}, nil
}

func (f *fakeSigningKeys) DeleteSecret(_ context.Context, in *secretsmanager.DeleteSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.DeleteSecretOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := aws.ToString(in.SecretId)
	if _, found := f.values[name]; !found {
		return nil, &smtypes.ResourceNotFoundException{}
	}
	delete(f.values, name)
	f.deleted = append(f.deleted, name)
	f.forced = append(f.forced, aws.ToBool(in.ForceDeleteWithoutRecovery))
	return &secretsmanager.DeleteSecretOutput{}, nil
}

func (f *fakeSigningKeys) ListSecrets(_ context.Context, in *secretsmanager.ListSecretsInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.ListSecretsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listings++
	var prefixes []string
	for _, filter := range in.Filters {
		if filter.Key == smtypes.FilterNameStringTypeName {
			prefixes = append(prefixes, filter.Values...)
		}
	}
	var names []string
	for name := range f.values {
		if slices.ContainsFunc(prefixes, func(prefix string) bool {
			return strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix))
		}) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	start := 0
	if in.NextToken != nil {
		start, _ = strconv.Atoi(*in.NextToken)
	}
	end := min(start+2, len(names))
	out := &secretsmanager.ListSecretsOutput{}
	for _, name := range names[start:end] {
		out.SecretList = append(out.SecretList, smtypes.SecretListEntry{Name: aws.String(name)})
	}
	if end < len(names) {
		out.NextToken = aws.String(strconv.Itoa(end))
	}
	return out, nil
}

func readSeed(t *testing.T, value string) []byte {
	t.Helper()
	seed, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("secret holds %q, want the base64 of an Ed25519 seed", value)
	}
	return seed
}

func TestARealtimeResourceWithNoKeyMintsAnEd25519SeedKeptTaggedInSecretsManager(t *testing.T) {
	t.Parallel()

	keys := newFakeSigningKeys()
	seed, err := ensureSigningKey(t.Context(), keys, "ocel/realtime/shop/prod/realtime--app")
	if err != nil {
		t.Fatalf("ensureSigningKey() = %v", err)
	}
	if got := readSeed(t, keys.values["ocel/realtime/shop/prod/realtime--app"]); !slices.Equal(got, seed) {
		t.Errorf("the secret holds %x, want the minted seed %x", got, seed)
	}
	if !slices.ContainsFunc(keys.tags["ocel/realtime/shop/prod/realtime--app"], func(tag smtypes.Tag) bool {
		return aws.ToString(tag.Key) == "ocel:managed-by" && aws.ToString(tag.Value) == "ocel"
	}) {
		t.Errorf("the secret is tagged %v, want ocel:managed-by=ocel so the deploy credentials may delete it", keys.tags["ocel/realtime/shop/prod/realtime--app"])
	}

	again, err := ensureSigningKey(t.Context(), keys, "ocel/realtime/shop/prod/realtime--app")
	if err != nil || !slices.Equal(again, seed) || keys.creates != 1 {
		t.Errorf("a second ensureSigningKey() = %x, %v after %d creates, want the kept seed and no second create", again, err, keys.creates)
	}
}

func TestAKeyAConcurrentDeployCreatedFirstIsTheOneKept(t *testing.T) {
	t.Parallel()

	keys := newFakeSigningKeys()
	winner := base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	keys.beforeCreate = func(name string) {
		keys.mu.Lock()
		keys.values[name] = winner
		keys.mu.Unlock()
	}
	seed, err := ensureSigningKey(t.Context(), keys, "ocel/realtime/shop/prod/realtime--app")
	if err != nil {
		t.Fatalf("ensureSigningKey() = %v", err)
	}
	if !slices.Equal(seed, make([]byte, ed25519.SeedSize)) {
		t.Errorf("ensureSigningKey() = %x, want the seed the concurrent deploy wrote", seed)
	}
}

func TestAPreviewOfAResourceWithNoKeyWritesNone(t *testing.T) {
	t.Parallel()

	keys := newFakeSigningKeys()
	seed, err := previewSigningKey(t.Context(), keys, "ocel/realtime/shop/prod/realtime--app")
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("previewSigningKey() = %x, %v, want a stand-in seed", seed, err)
	}
	if keys.creates != 0 {
		t.Errorf("a preview created %d secrets, want none", keys.creates)
	}
}

func TestAKeyThatIsNoSeedIsRefusedRatherThanSignedWith(t *testing.T) {
	t.Parallel()

	keys := newFakeSigningKeys()
	keys.values["ocel/realtime/shop/prod/realtime--app"] = "not-a-seed"
	if _, err := ensureSigningKey(t.Context(), keys, "ocel/realtime/shop/prod/realtime--app"); err == nil || !strings.Contains(err.Error(), "ocel/realtime/shop/prod/realtime--app") {
		t.Errorf("ensureSigningKey() = %v, want a refusal naming the secret", err)
	}
}

func TestOnlyTheKeysOfTheStageBeingDeployedAreListed(t *testing.T) {
	t.Parallel()

	keys := newFakeSigningKeys()
	for _, name := range []string{
		"ocel/realtime/shop/prod/realtime--app",
		"ocel/realtime/shop/prod/realtime--chat",
		"ocel/realtime/shop/prod/realtime--live",
		"ocel/realtime/shop/production/realtime--app",
		"ocel/realtime/shopfront/prod/realtime--app",
	} {
		keys.values[name] = "x"
	}
	listed, err := listSigningKeys(t.Context(), keys, signingKeyPath("ocel/realtime", "shop", "prod"))
	if err != nil {
		t.Fatalf("listSigningKeys() = %v", err)
	}
	want := []string{"ocel/realtime/shop/prod/realtime--app", "ocel/realtime/shop/prod/realtime--chat", "ocel/realtime/shop/prod/realtime--live"}
	if !slices.Equal(listed, want) {
		t.Errorf("listSigningKeys() = %v, want %v and no other stage's", listed, want)
	}
}

func TestDeletedKeysAreGoneAtOnceRatherThanHeldForRecovery(t *testing.T) {
	t.Parallel()

	keys := newFakeSigningKeys()
	keys.values["ocel/realtime/shop/prod/realtime--app"] = "x"
	if err := deleteSigningKeys(t.Context(), keys, []string{"ocel/realtime/shop/prod/realtime--app", "ocel/realtime/shop/prod/realtime--gone"}); err != nil {
		t.Fatalf("deleteSigningKeys() = %v, want a key already gone skipped", err)
	}
	if !slices.Equal(keys.deleted, []string{"ocel/realtime/shop/prod/realtime--app"}) || !slices.Equal(keys.forced, []bool{true}) {
		t.Errorf("deleted %v forced %v, want the key deleted without a recovery window", keys.deleted, keys.forced)
	}
}

func TestARealtimeBindingCarriesTheAPIsSocketHostKeysAndTwoScopedGrants(t *testing.T) {
	t.Parallel()

	keys := newFakeSigningKeys()
	seed := make([]byte, ed25519.SeedSize)
	seed[0] = 7
	keys.values["ocel/realtime/shop/prod/realtime--app"] = base64.StdEncoding.EncodeToString(seed)
	binding, err := collectRealtimeBinding(t.Context(), keys, "realtime--app", map[string]any{
		outputKeyHost:         "abc.appsync-api.eu-west-1.amazonaws.com",
		outputKeyRealtimeHost: "abc.appsync-realtime-api.eu-west-1.amazonaws.com",
		outputKeyAPIARN:       "arn:aws:appsync:eu-west-1:111122223333:apis/abcdefghij",
		outputKeyNamespace:    "app",
		outputKeySigningKey:   "ocel/realtime/shop/prod/realtime--app",
	})
	if err != nil {
		t.Fatalf("collectRealtimeBinding() = %v", err)
	}
	collected := bindingOf(provider.BindingRealtime, binding)
	for property, want := range map[string]string{
		provider.PropertyTransport:  "REALTIME_TRANSPORT_APPSYNC_EVENTS",
		provider.PropertyURL:        "wss://abc.appsync-realtime-api.eu-west-1.amazonaws.com/event/realtime",
		provider.PropertyHost:       "abc.appsync-api.eu-west-1.amazonaws.com",
		provider.PropertySigningKey: base64.StdEncoding.EncodeToString(seed),
		provider.PropertyVerifyKey:  base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)),
	} {
		if got := collected.Properties[property]; got != want {
			t.Errorf("binding %s = %q, want %q", property, got, want)
		}
	}
	if err := provider.VerifyProperties(collected); err != nil {
		t.Errorf("VerifyProperties() = %v, want the binding every realtime client reads", err)
	}
	if err := VerifyGrants(collected); err != nil {
		t.Errorf("VerifyGrants() = %v, want the grants scoped", err)
	}
	wantGrants := []provider.Grant{
		{Label: "connect", Actions: []string{"appsync:EventConnect"}, Resources: []string{"arn:aws:appsync:eu-west-1:111122223333:apis/abcdefghij"}},
		{Label: "publish", Actions: []string{"appsync:EventPublish"}, Resources: []string{"arn:aws:appsync:eu-west-1:111122223333:apis/abcdefghij/channelNamespace/app"}},
	}
	if len(collected.Grants) != len(wantGrants) {
		t.Fatalf("grants = %+v, want %+v", collected.Grants, wantGrants)
	}
	for i, want := range wantGrants {
		got := collected.Grants[i]
		if got.Label != want.Label || !slices.Equal(got.Actions, want.Actions) || !slices.Equal(got.Resources, want.Resources) {
			t.Errorf("grant %d = %+v, want %+v", i, got, want)
		}
	}
}

func TestARealtimeBindingWhoseKeyIsGoneIsRefusedNamingTheSecret(t *testing.T) {
	t.Parallel()

	_, err := collectRealtimeBinding(t.Context(), newFakeSigningKeys(), "realtime--app", map[string]any{
		outputKeyHost:         "abc.appsync-api.eu-west-1.amazonaws.com",
		outputKeyRealtimeHost: "abc.appsync-realtime-api.eu-west-1.amazonaws.com",
		outputKeyAPIARN:       "arn:aws:appsync:eu-west-1:111122223333:apis/abcdefghij",
		outputKeyNamespace:    "app",
		outputKeySigningKey:   "ocel/realtime/shop/prod/realtime--app",
	})
	if err == nil || !strings.Contains(err.Error(), "ocel/realtime/shop/prod/realtime--app") {
		t.Errorf("collectRealtimeBinding() = %v, want a refusal naming the missing secret", err)
	}
}
