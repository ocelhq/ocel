package live

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"google.golang.org/protobuf/encoding/protojson"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

type rotatingSecret struct {
	password string
	err      error
	reads    []string
}

func (s *rotatingSecret) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	s.reads = append(s.reads, *in.SecretId)
	if s.err != nil {
		return nil, s.err
	}
	value := fmt.Sprintf(`{"username":"ocel","password":%q}`, s.password)
	return &secretsmanager.GetSecretValueOutput{SecretString: &value}, nil
}

func publishedPostgresFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "proto", "provider", "variablestore", "v1", "fixtures", "postgres.json"))
	if err != nil {
		t.Fatalf("read the published postgres fixture: %v", err)
	}
	return raw
}

func fixtureSecretARN(t *testing.T) string {
	t.Helper()
	published := &variablestorev1.PublishedBinding{}
	if err := protojson.Unmarshal(publishedPostgresFixture(t), published); err != nil {
		t.Fatal(err)
	}
	secretARN := provider.PublishedBindingOf(published).PasswordSecret()
	if secretARN == "" {
		t.Fatalf("the published postgres fixture names no %q grant", provider.GrantPasswordSecret)
	}
	return secretARN
}

func deployedPostgres(t *testing.T, value []byte) ([]variablestore.StoredBinding, map[string]string) {
	t.Helper()
	records := []variablestore.StoredBinding{{Name: postgresBinding().Name, Value: value, Version: 1}}
	return records, live.MergeBindings(nil, []live.Binding{postgresBinding()}, records)
}

func withoutGrants(t *testing.T) []byte {
	t.Helper()
	published := &variablestorev1.PublishedBinding{}
	if err := protojson.Unmarshal(publishedPostgresFixture(t), published); err != nil {
		t.Fatal(err)
	}
	published.Grants = nil
	encoded, err := protojson.Marshal(published)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestCurrentPasswords(t *testing.T) {
	t.Parallel()

	bindings := []live.Binding{postgresBinding()}

	t.Run("a credential rotated after the deploy is served at the next read", func(t *testing.T) {
		t.Parallel()

		secret := &rotatingSecret{password: "rotated-once"}
		records, values := deployedPostgres(t, publishedPostgresFixture(t))
		if err := currentPasswords(context.Background(), passwordSources{secrets: secret}, bindings, records, values); err != nil {
			t.Fatalf("currentPasswords: %v", err)
		}
		if got := decodeBinding(t, values[postgresBinding().Key]).GetPostgres().GetPassword(); got != "rotated-once" {
			t.Errorf("password = %q, want the one the cluster's secret holds now: RDS rotates it every 7 days, so the deploy's copy stops working", got)
		}

		secret.password = "rotated-twice"
		records, values = deployedPostgres(t, publishedPostgresFixture(t))
		if err := currentPasswords(context.Background(), passwordSources{secrets: secret}, bindings, records, values); err != nil {
			t.Fatalf("currentPasswords: %v", err)
		}
		if got := decodeBinding(t, values[postgresBinding().Key]).GetPostgres().GetPassword(); got != "rotated-twice" {
			t.Errorf("password = %q, want each read to ask the secret again", got)
		}
		if want := fixtureSecretARN(t); len(secret.reads) != 2 || secret.reads[0] != want {
			t.Errorf("read %v, want the secret the published record's grant names, %s, once per read", secret.reads, want)
		}
	})

	t.Run("the app is handed the binding with its password and none of the grants", func(t *testing.T) {
		t.Parallel()

		secret := &rotatingSecret{password: "rotated-once"}
		records, values := deployedPostgres(t, publishedPostgresFixture(t))
		if err := currentPasswords(context.Background(), passwordSources{secrets: secret}, bindings, records, values); err != nil {
			t.Fatalf("currentPasswords: %v", err)
		}
		handed := values[postgresBinding().Key]
		if strings.Contains(handed, "secretsmanager") || strings.Contains(handed, fixtureSecretARN(t)) {
			t.Errorf("the app was handed %s, want the binding alone: grants are the deploy's to render, never the app's", handed)
		}
		if decodeBinding(t, handed).GetPostgres().GetHost() == "" {
			t.Errorf("the app was handed %s, want the published binding", handed)
		}
	})

	t.Run("a record naming no secret is handed over as published", func(t *testing.T) {
		t.Parallel()

		secret := &rotatingSecret{password: "unused"}
		records, values := deployedPostgres(t, withoutGrants(t))
		if err := currentPasswords(context.Background(), passwordSources{secrets: secret}, bindings, records, values); err != nil {
			t.Fatalf("currentPasswords: %v", err)
		}
		if got := decodeBinding(t, values[postgresBinding().Key]).GetPostgres().GetPassword(); got != "fixture-password-not-a-secret" {
			t.Errorf("password = %q, want the published one where no secret is named", got)
		}
		if len(secret.reads) != 0 {
			t.Errorf("read %v, want no secret read", secret.reads)
		}
	})

	t.Run("a secret that cannot be read fails the read", func(t *testing.T) {
		t.Parallel()

		secret := &rotatingSecret{err: errors.New("AccessDeniedException")}
		records, values := deployedPostgres(t, publishedPostgresFixture(t))
		if err := currentPasswords(context.Background(), passwordSources{secrets: secret}, bindings, records, values); err == nil {
			t.Error("currentPasswords = nil, want the failure surfaced rather than a password the cluster may have rotated away")
		}
	})

	t.Run("a binding of another type reads no secret", func(t *testing.T) {
		t.Parallel()

		secret := &rotatingSecret{password: "unused"}
		bucket := live.Binding{Name: "bucket--uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Type: bindingsv1.BindingType_BINDING_TYPE_BUCKET}
		records := []variablestore.StoredBinding{{Name: bucket.Name, Value: publishedPostgresFixture(t), Version: 1}}
		values := live.MergeBindings(nil, []live.Binding{bucket}, records)
		if err := currentPasswords(context.Background(), passwordSources{secrets: secret}, []live.Binding{bucket}, records, values); err != nil {
			t.Fatalf("currentPasswords: %v", err)
		}
		if len(secret.reads) != 0 {
			t.Errorf("read %v, want no secret read for a binding this deployment reads as a bucket", secret.reads)
		}
	})
}

func withIAMAuthentication(t *testing.T) []byte {
	t.Helper()
	published := &variablestorev1.PublishedBinding{}
	if err := protojson.Unmarshal(publishedPostgresFixture(t), published); err != nil {
		t.Fatal(err)
	}
	published.GetBinding().GetPostgres().Password = ""
	published.Grants = []*variablestorev1.Grant{{
		Label:     provider.GrantIAMAuthentication,
		Actions:   []string{"rds-db:connect"},
		Resources: []string{"arn:aws:rds-db:us-east-1:111122223333:dbuser:cluster-ABCDEFGHIJKL/fixture_operator"},
	}}
	encoded, err := protojson.Marshal(published)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestAnIAMAuthenticatedDatabaseIsHandedATokenMintedAtEachReadAndNoSecretIsRead(t *testing.T) {
	t.Parallel()

	secret := &rotatingSecret{password: "master-password"}
	var minted []string
	mint := func(_ context.Context, endpoint, username string) (string, error) {
		minted = append(minted, endpoint+" as "+username)
		return fmt.Sprintf("token-%d", len(minted)), nil
	}
	for want := range []string{"token-1", "token-2"} {
		records, values := deployedPostgres(t, withIAMAuthentication(t))
		if err := currentPasswords(context.Background(), passwordSources{secrets: secret, mintToken: mint}, []live.Binding{postgresBinding()}, records, values); err != nil {
			t.Fatalf("currentPasswords: %v", err)
		}
		if got := decodeBinding(t, values[postgresBinding().Key]).GetPostgres().GetPassword(); got != fmt.Sprintf("token-%d", want+1) {
			t.Errorf("password = %q, want the token minted at this read: a token lives 15 minutes, so each read mints its own", got)
		}
	}
	if want := "shop-prod-main-r1a2b3c4.cluster-cxyz.us-east-1.rds.amazonaws.com:5433 as fixture_operator"; len(minted) != 2 || minted[0] != want {
		t.Errorf("minted %v, want a token for %s at each read", minted, want)
	}
	if len(secret.reads) != 0 {
		t.Errorf("read secrets %v, want none: an app authenticated by IAM never reads the master password", secret.reads)
	}
}
