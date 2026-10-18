package postgres

import (
	"crypto/x509"
	"encoding/pem"
	"slices"
	"strings"
	"testing"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
)

const testSecretARN = "arn:aws:secretsmanager:us-east-1:111122223333:secret:rds!cluster-0a1b2c3d-AbCdEf"

func postgresOutputs() map[string]any {
	return map[string]any{
		component.OutputKeyHost: "shop-prod-main.cluster-cxyz.us-east-1.rds.amazonaws.com",
		component.OutputKeyPort: float64(Port),
		OutputKeyDatabase:       postgresDatabaseName,
		OutputKeyUsername:       postgresMasterUsername,
		OutputKeySecretARN:      testSecretARN,
	}
}

func collectTestPostgres(t *testing.T, region string) (*variablestorev1.PublishedBinding, error) {
	t.Helper()
	return collectBinding(region, "db--main", postgresOutputs())
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
		t.Fatalf("collectBinding: %v", err)
	}
	if got := provider.PublishedBindingOf(binding).PasswordSecret(); got != testSecretARN {
		t.Errorf("the binding names %q as its password's secret, want %s: RDS rotates the password there every 7 days, and the runtime reads it from there", got, testSecretARN)
	}
	at := slices.IndexFunc(binding.GetGrants(), func(g *variablestorev1.Grant) bool { return g.GetLabel() == provider.GrantPasswordSecret })
	if at < 0 {
		t.Fatalf("grants = %v, want one that lets the app read the master secret", binding.GetGrants())
	}
	grant := binding.GetGrants()[at]
	if !slices.Equal(grant.GetActions(), []string{"secretsmanager:GetSecretValue"}) || !slices.Equal(grant.GetResources(), []string{testSecretARN}) {
		t.Errorf("grant = %v, want GetSecretValue on the cluster's secret alone", grant)
	}
}

func TestThePostgresBindingCarriesNoPasswordARotationCouldLeaveStale(t *testing.T) {
	t.Parallel()

	binding, err := collectTestPostgres(t, "us-east-1")
	if err != nil {
		t.Fatalf("collectBinding: %v", err)
	}
	if got := binding.GetBinding().GetPostgres().GetPassword(); got != "" {
		t.Errorf("the binding carries a password of %d bytes, want none: RDS rotates it every 7 days, and every reader resolves it from the secret the binding names", len(got))
	}
	if err := provider.VerifyProperties(provider.PublishedBindingOf(binding)); err != nil {
		t.Errorf("VerifyProperties() = %v, want the binding providerserver records", err)
	}
}

func TestThePostgresBindingVerifiesTheServerAgainstItsRegionsRDSRoots(t *testing.T) {
	t.Parallel()

	binding, err := collectTestPostgres(t, "eu-west-1")
	if err != nil {
		t.Fatalf("collectBinding: %v", err)
	}
	properties := binding.GetBinding().GetPostgres()
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
		t.Errorf("collectBinding = %v, want a refusal naming the region whose RDS root it lacks", err)
	}
}

func TestPostgresProducerEmitsTheFixture(t *testing.T) {
	t.Parallel()

	want := componenttest.BindingFixture(t, bindingsv1.BindingType_BINDING_TYPE_POSTGRES).GetPostgres()
	secretARN := provider.PublishedBindingOf(componenttest.PublishedFixture(t, bindingsv1.BindingType_BINDING_TYPE_POSTGRES)).PasswordSecret()
	if secretARN == "" {
		t.Fatalf("fixture %s names no %q grant, and the AWS record names its password by one", componenttest.FixtureFile(bindingsv1.BindingType_BINDING_TYPE_POSTGRES), provider.GrantPasswordSecret)
	}

	fields := map[string]any{
		component.OutputKeyHost: want.GetHost(),
		component.OutputKeyPort: float64(want.GetPort()),
		OutputKeyDatabase:       want.GetDatabase(),
		OutputKeyUsername:       want.GetUsername(),
		OutputKeySecretARN:      secretARN,
	}

	got, err := collectBinding("us-east-1", "db--main", fields)
	if err != nil {
		t.Fatalf("collectPostgresBinding: %v", err)
	}
	if password := got.GetBinding().GetPostgres().GetPassword(); password != "" {
		t.Errorf("AWS emitted a password of %d bytes, want none: the runtime reads it from the secret the record names", len(password))
	}
	if named := provider.PublishedBindingOf(got).PasswordSecret(); named != secretARN {
		t.Errorf("AWS named %q as the password secret, want %s", named, secretARN)
	}
	got.GetBinding().GetPostgres().Password = want.GetPassword()
	componenttest.AssertMatchesFixture(t, got, bindingsv1.BindingType_BINDING_TYPE_POSTGRES)
}
