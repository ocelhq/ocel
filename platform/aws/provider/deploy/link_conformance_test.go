package deploy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/ocelhq/ocel/pkg/naming"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type stubSecrets struct{ secretString string }

func (s stubSecrets) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	value := s.secretString
	return &secretsmanager.GetSecretValueOutput{SecretString: &value, ARN: in.SecretId}, nil
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "proto", "links", "v1", "fixtures")
}

func fixtureFile(token string) string {
	return strings.ReplaceAll(token, ":", "-") + ".json"
}

func linkFixture(t *testing.T, token string) *linksv1.Link {
	t.Helper()
	path := filepath.Join(fixtureDir(t), fixtureFile(token))
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read link fixture for %s: %v", token, err)
	}
	link := &linksv1.Link{}
	if err := protojson.Unmarshal(body, link); err != nil {
		t.Fatalf("link fixture %s is not a links.v1.Link: %v", path, err)
	}
	return link
}

func TestLinkFixtureExistsPerOwnedToken(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(fixtureDir(t))
	if err != nil {
		t.Fatalf("read fixture directory: %v", err)
	}
	var found []string
	for _, e := range entries {
		found = append(found, e.Name())
	}

	var want []string
	for _, token := range naming.Tokens() {
		want = append(want, fixtureFile(token))
		if got := linkFixture(t, token).GetType(); got != token {
			t.Errorf("fixture %s carries type %q, want %q", fixtureFile(token), got, token)
		}
	}

	slices.Sort(found)
	slices.Sort(want)
	if !slices.Equal(found, want) {
		t.Errorf("fixtures = %v, want exactly one per ocel-owned token %v", found, want)
	}
}

func assertMatchesFixture(t *testing.T, got, want *linksv1.Link) {
	t.Helper()
	if !proto.Equal(got, want) {
		t.Errorf("producer emitted %v, want the checked-in record %v — the consumer suite parses that fixture, so a divergence here is cross-language drift", got, want)
	}
}

func TestPostgresProducerEmitsTheFixture(t *testing.T) {
	t.Parallel()

	want := linkFixture(t, naming.TokenPostgres)
	props := want.GetProperties()

	port, err := strconv.Atoi(props["port"])
	if err != nil {
		t.Fatalf("fixture port %q is not an integer: %v", props["port"], err)
	}
	secret, err := json.Marshal(map[string]string{
		"username": "fixture_secret_principal",
		"password": props["password"],
	})
	if err != nil {
		t.Fatalf("encode stub secret: %v", err)
	}

	fields := map[string]any{
		outputKeyHost:      props["host"],
		outputKeyPort:      float64(port),
		outputKeyDatabase:  props["database"],
		outputKeyUsername:  props["username"],
		outputKeySecretARN: "arn:aws:secretsmanager:us-east-1:111122223333:secret:shop-prod-main-AbCdEf",
	}

	got, err := collectPostgresLink(context.Background(), stubSecrets{secretString: string(secret)}, want.GetName(), fields)
	if err != nil {
		t.Fatalf("collectPostgresLink: %v", err)
	}
	assertMatchesFixture(t, got, want)
}

func TestBucketProducerEmitsTheFixture(t *testing.T) {
	t.Parallel()

	want := linkFixture(t, naming.TokenBucket)
	fields := map[string]any{outputKeyBucket: want.GetProperties()["bucket"]}

	got, err := collectBucketLink(want.GetName(), fields)
	if err != nil {
		t.Fatalf("collectBucketLink: %v", err)
	}
	assertMatchesFixture(t, got, want)
}
