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
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

func TestAnAppIsGrantedNoRecordsOrKeyOfTheNamespaceOrTheTier(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c := ensuringAccounts(t, server)

	email, err := p.ensureAppAccount(context.Background(), c, functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}), nil)
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

	email, err := p.ensureAppAccount(ctx, c, spec, nil)
	if err != nil {
		t.Fatal(err)
	}

	var reached []string
	for _, bound := range projectBindingsOf(server) {
		if !strings.Contains(bound, email) {
			continue
		}
		for _, project := range []string{"shop", "billing", "blog"} {
			if strings.Contains(bound, ports.ProjectDatabase("ocel", tier, project)) || strings.Contains(bound, ports.ProjectKey(tier, project)) {
				reached = append(reached, project)
			}
		}
	}
	slices.Sort(reached)
	if want := []string{"billing", "billing", "shop", "shop"}; !slices.Equal(reached, want) {
		t.Errorf("the app's grants reach the records and keys of %q, want shop's and billing's, whose value shop's STRIPE_KEY references, and never blog's", reached)
	}
}

type projectKeysKMS struct {
	kmspb.UnimplementedKeyManagementServiceServer

	mu      sync.Mutex
	keys    map[string]bool
	created []string
}

func (k *projectKeysKMS) CreateCryptoKey(_ context.Context, req *kmspb.CreateCryptoKeyRequest) (*kmspb.CryptoKey, error) {
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

func (k *projectKeysKMS) Encrypt(_ context.Context, req *kmspb.EncryptRequest) (*kmspb.EncryptResponse, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.keys[req.GetName()] {
		return nil, status.Error(codes.NotFound, req.GetName())
	}
	return &kmspb.EncryptResponse{Name: req.GetName(), Ciphertext: []byte("sealed")}, nil
}

func servingProjectKeys(t *testing.T) (*Provider, *projectKeysKMS) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	kms := &projectKeysKMS{keys: map[string]bool{"projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/production": true}}
	server := grpc.NewServer()
	kmspb.RegisterKeyManagementServiceServer(server, kms)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return pushing(t, "http://"+listener.Addr().String()), kms
}

func TestTheFirstValueAProjectSealsMakesTheProjectsKeyAndIsSealedUnderIt(t *testing.T) {
	t.Parallel()
	p, kms := servingProjectKeys(t)
	bound := seal.BindToProject("shop", "variable", "STRIPE_KEY")

	for range 2 {
		if _, err := p.Cipher().Seal(context.Background(), environment.TierProduction, bound, []byte("sk_live")); err != nil {
			t.Fatalf("Seal() = %v, want the project's key made and the value sealed under it", err)
		}
	}

	if want := []string{ports.ProjectKey(environment.TierProduction, "shop")}; !slices.Equal(kms.created, want) {
		t.Errorf("made the keys %q, want %q once: the second seal finds the key there", kms.created, want)
	}
}

func TestAValueOfEveryProjectIsSealedUnderTheTierKeyAndMakesNoKey(t *testing.T) {
	t.Parallel()
	p, kms := servingProjectKeys(t)

	if _, err := p.Cipher().Seal(context.Background(), environment.TierProduction, seal.Bind("gates"), []byte("v")); err != nil {
		t.Fatalf("Seal() = %v", err)
	}

	if len(kms.created) != 0 {
		t.Errorf("made the keys %q, want none: a value no one project owns is sealed under the tier key", kms.created)
	}
}
