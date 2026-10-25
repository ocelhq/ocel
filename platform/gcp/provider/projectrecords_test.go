package gcp

import (
	"context"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

func TestAnAppThatOpensNoValueIsGrantedNoRecordsOrKeyOfTheNamespaceOrTheTier(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c := ensuringAccounts(t, server)

	email, _, err := p.release().EnsureAccount(context.Background(), c, functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}), nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, bound := range projectBindingsOf(server) {
		if strings.Contains(bound, email) && (strings.HasSuffix(bound, `/databases/ocel"`) || strings.HasSuffix(bound, `/cryptoKeys/production"`)) {
			t.Errorf("the app's account holds %q, and that database and key hold every project of the tier", bound)
		}
	}
}

func TestAnAppWhoseValueReferencesAnotherProjectMayReadAndOpenThatProjectsRecordsAndNoOthers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	server := appAccountsOnly()
	p, c := ensuringAccounts(t, server)
	store := variablestore.Store{KeyValues: p.KeyValues(), Cipher: fake.NewCipher()}
	tier := environment.TierProduction
	stripe := variablestore.Cell{Key: "STRIPE_KEY"}
	if _, err := store.Set(ctx, variablestore.Scope{Project: "billing", Tier: tier}, variablestore.Coordinate{Cell: stripe}, "sk_live", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(ctx, variablestore.Scope{Project: "blog", Tier: tier}, variablestore.Coordinate{Cell: stripe}, "sk_blog", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetReference(ctx, variablestore.Scope{Project: "shop", Tier: tier}, variablestore.Coordinate{Cell: stripe},
		variablestore.Target{Project: "billing", Cell: stripe}); err != nil {
		t.Fatal(err)
	}
	spec := functionStackDeclaring(tier, "production", provider.AppValues{Secrets: []provider.SecretRef{{Key: "STRIPE_KEY"}}})
	databases := map[string]string{
		"shop":    ports.SharedDatabase("ocel", tier),
		"billing": recordOwnDatabase(t, c, tier, "billing"),
		"blog":    recordOwnDatabase(t, c, tier, "blog"),
	}

	email, _, err := p.release().EnsureAccount(ctx, c, spec, nil)
	if err != nil {
		t.Fatal(err)
	}

	var reached []string
	for _, bound := range projectBindingsOf(server) {
		if !strings.Contains(bound, email) {
			continue
		}
		for project, database := range databases {
			if strings.Contains(bound, "/databases/"+database+`"`) {
				reached = append(reached, project)
			}
		}
	}
	slices.Sort(reached)
	if want := []string{"billing", "shop"}; !slices.Equal(reached, want) {
		t.Errorf("the app's grants reach the records of %q, want shop's and billing's, whose value shop's STRIPE_KEY references, and never blog's", reached)
	}
	opening := []string{"roles/cloudkms.cryptoKeyDecrypter serviceAccount:" + email + " on " + gcloud.KeyPath(c, string(tier))}
	if got := keyBindingsOf(server); !slices.Equal(got, opening) {
		t.Errorf("the keys bind %q, want exactly %q: shop's and billing's values are both sealed under the tier key", got, opening)
	}
}

type keysKMS struct {
	kmspb.UnimplementedKeyManagementServiceServer

	mu          sync.Mutex
	keys        map[string]bool
	created     []string
	sealedUnder []string

	encryptUnserved int
}

func (k *keysKMS) GetCryptoKey(_ context.Context, req *kmspb.GetCryptoKeyRequest) (*kmspb.CryptoKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.keys[req.GetName()] {
		return nil, status.Error(codes.NotFound, req.GetName())
	}
	return &kmspb.CryptoKey{Name: req.GetName(), Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT,
		Primary: &kmspb.CryptoKeyVersion{Name: req.GetName() + "/cryptoKeyVersions/1", State: kmspb.CryptoKeyVersion_ENABLED}}, nil
}

func (k *keysKMS) CreateCryptoKey(_ context.Context, req *kmspb.CreateCryptoKeyRequest) (*kmspb.CryptoKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	name := req.GetParent() + "/cryptoKeys/" + req.GetCryptoKeyId()
	if k.keys[name] {
		return nil, status.Error(codes.AlreadyExists, name)
	}
	k.keys[name] = true
	k.created = append(k.created, req.GetCryptoKeyId())
	return &kmspb.CryptoKey{Name: name}, nil
}

func (k *keysKMS) Encrypt(_ context.Context, req *kmspb.EncryptRequest) (*kmspb.EncryptResponse, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.keys[req.GetName()] {
		return nil, status.Error(codes.NotFound, req.GetName())
	}
	if k.encryptUnserved > 0 {
		k.encryptUnserved--
		return nil, status.Error(codes.NotFound, req.GetName())
	}
	k.sealedUnder = append(k.sealedUnder, req.GetName())
	return &kmspb.EncryptResponse{Name: req.GetName(), Ciphertext: []byte("sealed")}, nil
}

const productionKey = "projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/production"

func servingTheTierKey(t *testing.T) (*Provider, *keysKMS) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	kms := &keysKMS{keys: map[string]bool{productionKey: true}}
	server := grpc.NewServer()
	kmspb.RegisterKeyManagementServiceServer(server, kms)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return pushing(t, "http://"+listener.Addr().String()), kms
}

func TestEveryValueOfATierIsSealedUnderTheTierKeyAndMakesNoKey(t *testing.T) {
	t.Parallel()
	p, kms := servingTheTierKey(t)
	recordedCurrent(t, p, environment.TierProduction, bootstrap.Keys)

	for _, bound := range []seal.AssociatedData{
		seal.BindToProject("shop", "variable", "STRIPE_KEY"),
		seal.BindToProject("blog", "variable", "STRIPE_KEY"),
		seal.Bind("gates"),
	} {
		if _, err := p.Cipher().Seal(context.Background(), environment.TierProduction, bound, []byte("v")); err != nil {
			t.Fatalf("Seal() = %v", err)
		}
	}

	if len(kms.created) != 0 {
		t.Errorf("made the keys %q, want none: every value of the tier is sealed under the tier key", kms.created)
	}
	if want := []string{productionKey, productionKey, productionKey}; !slices.Equal(kms.sealedUnder, want) {
		t.Errorf("sealed under %q, want the tier key each time", kms.sealedUnder)
	}
}

func TestAValueSealedUnderATierKeyCloudKMSDoesNotServeYetIsSealedOnceItDoes(t *testing.T) {
	t.Parallel()
	p, kms := servingTheTierKey(t)
	kms.encryptUnserved = 2
	recordedCurrent(t, p, environment.TierProduction, bootstrap.Keys)

	if _, err := p.Cipher().Seal(context.Background(), environment.TierProduction, seal.BindToProject("shop", "variable", "STRIPE_KEY"), []byte("sk_live")); err != nil {
		t.Fatalf("Seal() = %v, want the value sealed once Cloud KMS serves the tier key", err)
	}
}
