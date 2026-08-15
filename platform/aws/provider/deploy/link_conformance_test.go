package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/ocelhq/ocel/pkg/naming"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/vars"
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

func fixtureFile(typ linksv1.LinkType) string {
	return strings.ToLower(naming.EnvFragment(typ)) + ".json"
}

func fixtureBytes(t *testing.T, typ linksv1.LinkType) []byte {
	t.Helper()
	path := filepath.Join(fixtureDir(t), fixtureFile(typ))
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read link fixture for %s: %v", typ, err)
	}
	return body
}

func linkFixture(t *testing.T, typ linksv1.LinkType) *linksv1.Link {
	t.Helper()
	link := &linksv1.Link{}
	if err := protojson.Unmarshal(fixtureBytes(t, typ), link); err != nil {
		t.Fatalf("link fixture %s is not a links.v1.Link: %v", fixtureFile(typ), err)
	}
	return link
}

func canonicalJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
	out, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("re-encode JSON: %v", err)
	}
	return out
}

func TestLinkFixtureExistsPerLinkType(t *testing.T) {
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
	for _, typ := range naming.LinkTypes() {
		want = append(want, fixtureFile(typ))
		if got := naming.LinkTypeOf(linkFixture(t, typ)); got != typ {
			t.Errorf("fixture %s carries a %s, want %s", fixtureFile(typ), got, typ)
		}
	}

	slices.Sort(found)
	slices.Sort(want)
	if !slices.Equal(found, want) {
		t.Errorf("fixtures = %v, want exactly one per link type %v", found, want)
	}
}

func assertMatchesFixture(t *testing.T, got *linksv1.Link, typ linksv1.LinkType) {
	t.Helper()
	want := linkFixture(t, typ)
	if !proto.Equal(got, want) {
		t.Errorf("producer emitted %v, want the checked-in record %v — the consumer suite parses that fixture, so a divergence here is cross-language drift", got, want)
	}

	payload, err := vars.EncodeLink(got)
	if err != nil {
		t.Fatalf("encode the produced link as the store and the app payload do: %v", err)
	}
	if wantBytes := canonicalJSON(t, fixtureBytes(t, typ)); !bytes.Equal(canonicalJSON(t, payload), wantBytes) {
		t.Errorf("app payload = %s, want the checked-in fixture byte for byte %s", payload, wantBytes)
	}
}

func TestPostgresProducerEmitsTheFixture(t *testing.T) {
	t.Parallel()

	want := linkFixture(t, linksv1.LinkType_LINK_TYPE_POSTGRES).GetPostgres()

	secret, err := json.Marshal(map[string]string{
		"username": "fixture_secret_principal",
		"password": want.GetPassword(),
	})
	if err != nil {
		t.Fatalf("encode stub secret: %v", err)
	}

	fields := map[string]any{
		outputKeyHost:      want.GetHost(),
		outputKeyPort:      float64(want.GetPort()),
		outputKeyDatabase:  want.GetDatabase(),
		outputKeyUsername:  want.GetUsername(),
		outputKeySecretARN: "arn:aws:secretsmanager:us-east-1:111122223333:secret:shop-prod-main-AbCdEf",
	}

	got, err := collectPostgresLink(context.Background(), stubSecrets{secretString: string(secret)}, "db--main", fields)
	if err != nil {
		t.Fatalf("collectPostgresLink: %v", err)
	}
	assertMatchesFixture(t, got, linksv1.LinkType_LINK_TYPE_POSTGRES)
}

const fixtureStateTableARN = "arn:aws:dynamodb:us-east-1:111122223333:table/ocel-state"

var fixtureSessions = newSessionScope("shop", "prod", fixtureStateTableARN)

func TestBucketProducerEmitsTheFixture(t *testing.T) {
	t.Parallel()

	want := linkFixture(t, linksv1.LinkType_LINK_TYPE_BUCKET).GetBucket()
	fields := map[string]any{outputKeyBucket: want.GetBucket()}

	got, err := collectBucketLink("bucket--uploads", fixtureSessions, fields)
	if err != nil {
		t.Fatalf("collectBucketLink: %v", err)
	}
	assertMatchesFixture(t, got, linksv1.LinkType_LINK_TYPE_BUCKET)
}
