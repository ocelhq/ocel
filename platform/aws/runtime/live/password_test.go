package live

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"google.golang.org/protobuf/encoding/protojson"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/live"
)

const masterSecretARN = "arn:aws:secretsmanager:us-east-1:111122223333:secret:rds!cluster-0a1b2c3d-AbCdEf"

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

func deployedPostgres(t *testing.T, grants ...*bindingsv1.Grant) map[string]string {
	t.Helper()
	encoded, err := protojson.Marshal(&bindingsv1.Binding{
		Name:       "db--main",
		Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{Host: "h", Database: "d", Username: "ocel", Password: "copied-at-deploy"}},
		Grants:     grants,
	})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{postgresBinding().Key: string(encoded)}
}

func masterSecretGrant() *bindingsv1.Grant {
	return &bindingsv1.Grant{Label: provider.GrantPasswordSecret, Actions: []string{"secretsmanager:GetSecretValue"}, Resources: []string{masterSecretARN}}
}

func TestCurrentPasswords(t *testing.T) {
	t.Parallel()

	bindings := []live.Binding{postgresBinding()}

	t.Run("a credential rotated after the deploy is served at the next read", func(t *testing.T) {
		t.Parallel()

		secret := &rotatingSecret{password: "rotated-once"}
		values := deployedPostgres(t, masterSecretGrant())
		if err := currentPasswords(context.Background(), secret, bindings, values); err != nil {
			t.Fatalf("currentPasswords: %v", err)
		}
		if got := decodeBinding(t, values[postgresBinding().Key]).GetPostgres().GetPassword(); got != "rotated-once" {
			t.Errorf("password = %q, want the one the cluster's secret holds now: RDS rotates it every 7 days, so the deploy's copy stops working", got)
		}

		secret.password = "rotated-twice"
		values = deployedPostgres(t, masterSecretGrant())
		if err := currentPasswords(context.Background(), secret, bindings, values); err != nil {
			t.Fatalf("currentPasswords: %v", err)
		}
		if got := decodeBinding(t, values[postgresBinding().Key]).GetPostgres().GetPassword(); got != "rotated-twice" {
			t.Errorf("password = %q, want each read to ask the secret again", got)
		}
		if len(secret.reads) != 2 || secret.reads[0] != masterSecretARN {
			t.Errorf("read %v, want the master secret the binding names, once per read", secret.reads)
		}
	})

	t.Run("a record naming no secret is handed over as published", func(t *testing.T) {
		t.Parallel()

		secret := &rotatingSecret{password: "unused"}
		values := deployedPostgres(t)
		if err := currentPasswords(context.Background(), secret, bindings, values); err != nil {
			t.Fatalf("currentPasswords: %v", err)
		}
		if got := decodeBinding(t, values[postgresBinding().Key]).GetPostgres().GetPassword(); got != "copied-at-deploy" {
			t.Errorf("password = %q, want the published one where no secret is named", got)
		}
		if len(secret.reads) != 0 {
			t.Errorf("read %v, want no secret read", secret.reads)
		}
	})

	t.Run("a secret that cannot be read fails the read", func(t *testing.T) {
		t.Parallel()

		secret := &rotatingSecret{err: errors.New("AccessDeniedException")}
		if err := currentPasswords(context.Background(), secret, bindings, deployedPostgres(t, masterSecretGrant())); err == nil {
			t.Error("currentPasswords = nil, want the failure surfaced rather than a password the cluster may have rotated away")
		}
	})
}
