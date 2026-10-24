package gcp

import (
	"context"
	"errors"
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
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
)

func TestAnAppIsGrantedNoRecordsOrKeyOfTheNamespaceOrTheTier(t *testing.T) {
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
	for _, bound := range slices.Concat(projectBindingsOf(server), keyBindingsOf(server)) {
		if !strings.Contains(bound, email) {
			continue
		}
		for project, database := range databases {
			if strings.Contains(bound, "/databases/"+database+`"`) || strings.Contains(bound, ports.ProjectKey(tier, project)) {
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

	mu       sync.Mutex
	keys     map[string]bool
	planted  map[string]*kmspb.CryptoKey
	created  []string
	ringless bool

	encryptUnserved int
}

func (k *projectKeysKMS) GetCryptoKey(_ context.Context, req *kmspb.GetCryptoKeyRequest) (*kmspb.CryptoKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if planted := k.planted[req.GetName()]; planted != nil {
		return planted, nil
	}
	if !k.keys[req.GetName()] {
		return nil, status.Error(codes.NotFound, req.GetName())
	}
	return &kmspb.CryptoKey{Name: req.GetName(), Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT,
		Primary: &kmspb.CryptoKeyVersion{Name: req.GetName() + "/cryptoKeyVersions/1", State: kmspb.CryptoKeyVersion_ENABLED}}, nil
}

func (k *projectKeysKMS) CreateCryptoKey(_ context.Context, req *kmspb.CreateCryptoKeyRequest) (*kmspb.CryptoKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.ringless {
		return nil, status.Error(codes.NotFound, "KeyRing not found: "+req.GetParent())
	}
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
	if k.encryptUnserved > 0 {
		k.encryptUnserved--
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
	recordedCurrent(t, p, environment.TierProduction, bootstrap.Keys)
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

func TestAValueSealedUnderAProjectKeyCloudKMSDoesNotServeYetIsSealedOnceItDoes(t *testing.T) {
	t.Parallel()
	p, kms := servingProjectKeys(t)
	kms.encryptUnserved = 2
	recordedCurrent(t, p, environment.TierProduction, bootstrap.Keys)

	if _, err := p.Cipher().Seal(context.Background(), environment.TierProduction, seal.BindToProject("shop", "variable", "STRIPE_KEY"), []byte("sk_live")); err != nil {
		t.Fatalf("Seal() = %v, want the value sealed once Cloud KMS serves the key it just made", err)
	}
}

func TestAProjectKeyThatCannotBeMadeWhereNoKeyRingExistsIsRefusedNamingThePartThatHoldsIt(t *testing.T) {
	t.Parallel()
	p, kms := servingProjectKeys(t)
	kms.ringless = true

	err := variables.EnsureProjectKey(context.Background(), p.resolved, environment.TierProduction, "shop")
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady || !strings.Contains(refused.Message, bootstrap.KeysPart) {
		t.Fatalf("EnsureProjectKey() where no key ring exists = %v, want a %s refusal naming the %s part that holds the ring", err, refusal.CodeNotReady, bootstrap.KeysPart)
	}
}

func TestAValueOfEveryProjectIsSealedUnderTheTierKeyAndMakesNoKey(t *testing.T) {
	t.Parallel()
	p, kms := servingProjectKeys(t)
	recordedCurrent(t, p, environment.TierProduction, bootstrap.Keys)

	if _, err := p.Cipher().Seal(context.Background(), environment.TierProduction, seal.Bind("gates"), []byte("v")); err != nil {
		t.Fatalf("Seal() = %v", err)
	}

	if len(kms.created) != 0 {
		t.Errorf("made the keys %q, want none: a value no one project owns is sealed under the tier key", kms.created)
	}
}

func TestAProjectKeyFoundMadeWithNoVersionToSealUnderIsRefused(t *testing.T) {
	t.Parallel()
	p, kms := servingProjectKeys(t)
	name := "projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/" + ports.ProjectKey(environment.TierProduction, "shop")
	for label, planted := range map[string]*kmspb.CryptoKey{
		"no primary version":           {Name: name, Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT},
		"a disabled primary":           {Name: name, Purpose: kmspb.CryptoKey_ENCRYPT_DECRYPT, Primary: &kmspb.CryptoKeyVersion{Name: name + "/cryptoKeyVersions/1", State: kmspb.CryptoKeyVersion_DISABLED}},
		"another purpose than sealing": {Name: name, Purpose: kmspb.CryptoKey_ASYMMETRIC_SIGN},
	} {
		kms.mu.Lock()
		kms.keys[name], kms.planted = true, map[string]*kmspb.CryptoKey{name: planted}
		kms.mu.Unlock()

		err := variables.EnsureProjectKey(context.Background(), p.resolved, environment.TierProduction, "shop")

		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(refused.Message, name) {
			t.Errorf("EnsureProjectKey() over a key with %s = %v, want a %s refusal naming %s: no value of shop can be sealed under it", label, err, refusal.CodeInvalid, name)
		}
	}
}

func TestAProjectKeyFoundMadeWithAnEnabledPrimaryVersionIsKept(t *testing.T) {
	t.Parallel()
	p, kms := servingProjectKeys(t)
	kms.keys["projects/acme-prod/locations/europe-west1/keyRings/ocel/cryptoKeys/"+ports.ProjectKey(environment.TierProduction, "shop")] = true

	if err := variables.EnsureProjectKey(context.Background(), p.resolved, environment.TierProduction, "shop"); err != nil {
		t.Errorf("EnsureProjectKey() over a key that seals = %v, want it kept", err)
	}
}
