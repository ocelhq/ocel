package deploy

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"slices"
	"strings"
	"testing"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	variables "github.com/ocelhq/ocel/platform/aws/provider/variables/live"
)

const testSecretARN = "arn:aws:secretsmanager:us-east-1:111122223333:secret:rds!cluster-0a1b2c3d-AbCdEf"

func postgresOutputs() map[string]any {
	return map[string]any{
		outputKeyHost:      "shop-prod-main.cluster-cxyz.us-east-1.rds.amazonaws.com",
		outputKeyPort:      float64(PostgresPort),
		outputKeyDatabase:  postgresDatabaseName,
		outputKeyUsername:  postgresMasterUsername,
		outputKeySecretARN: testSecretARN,
	}
}

func collectTestPostgres(t *testing.T, region string) (*bindingsv1.Binding, error) {
	t.Helper()
	secret, err := json.Marshal(map[string]string{"username": postgresMasterUsername, "password": "at-deploy"})
	if err != nil {
		t.Fatal(err)
	}
	return collectPostgresBinding(context.Background(), stubSecrets{secretString: string(secret)}, region, "db--main", postgresOutputs())
}

func certificateNames(t *testing.T, bundle string) []string {
	t.Helper()
	var names []string
	rest := []byte(bundle)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("the CA holds a block that is no certificate: %v", err)
		}
		names = append(names, certificate.Subject.CommonName)
	}
	return names
}

func TestThePostgresBindingNamesTheSecretItsPasswordRotatesIn(t *testing.T) {
	t.Parallel()

	binding, err := collectTestPostgres(t, "us-east-1")
	if err != nil {
		t.Fatalf("collectPostgresBinding: %v", err)
	}
	if got := variables.PostgresPasswordSecret(binding); got != testSecretARN {
		t.Errorf("the binding names %q as its password's secret, want %s: RDS rotates the password there every 7 days, and the runtime reads it from there", got, testSecretARN)
	}
	at := slices.IndexFunc(binding.GetGrants(), func(g *bindingsv1.Grant) bool { return g.GetLabel() == variables.PostgresPasswordGrant })
	if at < 0 {
		t.Fatalf("grants = %v, want one that lets the app read the master secret", binding.GetGrants())
	}
	grant := binding.GetGrants()[at]
	if !slices.Equal(grant.GetActions(), []string{"secretsmanager:GetSecretValue"}) || !slices.Equal(grant.GetResources(), []string{testSecretARN}) {
		t.Errorf("grant = %v, want GetSecretValue on the cluster's secret alone", grant)
	}
}

func TestThePostgresBindingVerifiesTheServerAgainstItsRegionsRDSRoots(t *testing.T) {
	t.Parallel()

	binding, err := collectTestPostgres(t, "eu-west-1")
	if err != nil {
		t.Fatalf("collectPostgresBinding: %v", err)
	}
	properties := binding.GetPostgres()
	if properties.GetTlsMode() != bindingsv1.PostgresTlsMode_POSTGRES_TLS_MODE_VERIFY_FULL {
		t.Errorf("tls mode = %s, want VERIFY_FULL so an SDK refuses a server that is not the cluster", properties.GetTlsMode())
	}
	names := certificateNames(t, properties.GetTlsCa())
	if len(names) == 0 {
		t.Fatal("the binding names no CA, and no system pool trusts the RDS roots")
	}
	for _, name := range names {
		if !strings.HasPrefix(name, "Amazon RDS eu-west-1 Root CA ") {
			t.Errorf("the CA trusts %q, want only the RDS roots of eu-west-1", name)
		}
	}
}

func TestAPostgresInARegionWithNoKnownRDSRootIsRefused(t *testing.T) {
	t.Parallel()

	if _, err := collectTestPostgres(t, "xx-nowhere-9"); err == nil || !strings.Contains(err.Error(), "xx-nowhere-9") {
		t.Errorf("collectPostgresBinding = %v, want a refusal naming the region whose RDS root it lacks", err)
	}
}
