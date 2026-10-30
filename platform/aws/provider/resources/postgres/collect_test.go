package postgres

import (
	"crypto/x509"
	"encoding/pem"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
)

const (
	testClusterARN = "arn:aws:rds:us-east-1:111122223333:cluster:ocel-shop-prod-main"
	testResourceID = "cluster-ABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

func postgresOutputs() map[string]any {
	return map[string]any{
		component.OutputKeyHost: "shop-prod-main.cluster-cxyz.us-east-1.rds.amazonaws.com",
		component.OutputKeyPort: float64(Port),
		OutputKeyDatabase:       postgresDatabaseName,
		OutputKeyUsername:       AppRoleFor(environment.TierProduction),
		OutputKeyClusterARN:     testClusterARN,
		OutputKeyResourceID:     testResourceID,
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

func TestThePostgresBindingConnectsAsTheAppRoleByIAMAndMayReachNoSecret(t *testing.T) {
	t.Parallel()

	binding, err := collectTestPostgres(t, "us-east-1")
	if err != nil {
		t.Fatalf("collectBinding: %v", err)
	}
	if got := binding.GetBinding().GetPostgres().GetUsername(); got != AppRoleFor(environment.TierProduction) {
		t.Errorf("username = %q, want the app role %s: an app never connects as the master user", got, AppRoleFor(environment.TierProduction))
	}
	if got := binding.GetBinding().GetPostgres().GetPassword(); got != "" {
		t.Errorf("the binding carries a password of %d bytes, want none: the app's role mints a 15-minute token instead", len(got))
	}
	want := "arn:aws:rds-db:us-east-1:111122223333:dbuser:" + testResourceID + "/" + AppRoleFor(environment.TierProduction)
	if len(binding.GetGrants()) != 1 {
		t.Fatalf("grants = %v, want exactly the IAM authentication grant", binding.GetGrants())
	}
	grant := binding.GetGrants()[0]
	if grant.GetLabel() != provider.GrantIAMAuthentication || !slices.Equal(grant.GetActions(), []string{"rds-db:connect"}) || !slices.Equal(grant.GetResources(), []string{want}) {
		t.Errorf("grant = %v, want rds-db:connect on %s alone", grant, want)
	}
	for _, grant := range binding.GetGrants() {
		for _, action := range grant.GetActions() {
			if strings.HasPrefix(action, "secretsmanager:") {
				t.Errorf("grant %v reaches Secrets Manager, want an app kept from the master secret", grant)
			}
		}
	}
	if err := provider.VerifyProperties(provider.PublishedBindingOf(binding)); err != nil {
		t.Errorf("VerifyProperties() = %v, want the binding providerserver records", err)
	}
}

func TestThePostgresBindingVerifiesTheServerAgainstTheRootOfTheCATheInstanceIsPinnedTo(t *testing.T) {
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
	if want := []string{"Amazon RDS eu-west-1 Root CA RSA2048 G1"}; !slices.Equal(names, want) {
		t.Errorf("the CA trusts %q, want %q, the root of %s alone", names, want, serverCA)
	}
}

func TestAPostgresBindingFitsTheLimitOfABoundValue(t *testing.T) {
	t.Parallel()

	for _, region := range []string{"us-east-1", "ap-southeast-7"} {
		binding, err := collectTestPostgres(t, region)
		if err != nil {
			t.Fatalf("collectBinding(%s): %v", region, err)
		}
		if _, err := variablestoreserver.BindingPair("shop", binding); err != nil {
			t.Errorf("BindingPair(%s) = %v, want the binding stored", region, err)
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
	fields := map[string]any{
		component.OutputKeyHost: want.GetHost(),
		component.OutputKeyPort: float64(want.GetPort()),
		OutputKeyDatabase:       want.GetDatabase(),
		OutputKeyUsername:       want.GetUsername(),
		OutputKeyClusterARN:     testClusterARN,
		OutputKeyResourceID:     testResourceID,
	}

	got, err := collectBinding("us-east-1", "db--main", fields)
	if err != nil {
		t.Fatalf("collectBinding: %v", err)
	}
	if !provider.PublishedBindingOf(got).HasIAMAuthentication() {
		t.Fatalf("grants = %v, want the record to say its app authenticates by IAM", got.GetGrants())
	}
	got.GetBinding().GetPostgres().Password = want.GetPassword()
	got.Grants = componenttest.PublishedFixture(t, bindingsv1.BindingType_BINDING_TYPE_POSTGRES).GetGrants()
	componenttest.AssertMatchesFixture(t, got, bindingsv1.BindingType_BINDING_TYPE_POSTGRES)
}
