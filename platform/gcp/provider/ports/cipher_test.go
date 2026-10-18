package ports

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"net"
	"slices"
	"sync"
	"testing"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	boundBytes = "8:variable,4:shop,10:production,7:staging,4:/web,14:STRIPE_API_KEY,"
	ringPath   = "projects/acme/locations/us-central1/keyRings/ocel"
	tierKey    = ringPath + "/cryptoKeys/production"
)

var sealedKey = ringPath + "/cryptoKeys/" + ProjectKey(environment.TierProduction, "shop")

type recordingKMS struct {
	kmspb.UnimplementedKeyManagementServiceServer

	mu        sync.Mutex
	encrypted []*kmspb.EncryptRequest
}

func (k *recordingKMS) Encrypt(_ context.Context, req *kmspb.EncryptRequest) (*kmspb.EncryptResponse, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.encrypted = append(k.encrypted, req)
	return &kmspb.EncryptResponse{Name: req.GetName() + "/cryptoKeyVersions/1", Ciphertext: []byte("sealed")}, nil
}

func (k *recordingKMS) Decrypt(_ context.Context, req *kmspb.DecryptRequest) (*kmspb.DecryptResponse, error) {
	if req.GetName() != sealedKey || string(req.GetAdditionalAuthenticatedData()) != boundBytes || string(req.GetCiphertext()) != "sealed" {
		return nil, status.Error(codes.InvalidArgument, "Decryption failed: the ciphertext is invalid")
	}
	return &kmspb.DecryptResponse{Plaintext: []byte("sk_live_secret")}, nil
}

type aeadKMS struct {
	kmspb.UnimplementedKeyManagementServiceServer

	mu   sync.Mutex
	keys map[string]cipher.AEAD
}

func (k *aeadKMS) ensureKey(name string) (cipher.AEAD, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if gcm, ok := k.keys[name]; ok {
		return gcm, nil
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(secret)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	k.keys[name] = gcm
	return gcm, nil
}

func (k *aeadKMS) Encrypt(_ context.Context, req *kmspb.EncryptRequest) (*kmspb.EncryptResponse, error) {
	gcm, err := k.ensureKey(req.GetName())
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return &kmspb.EncryptResponse{Ciphertext: gcm.Seal(nonce, nonce, req.GetPlaintext(), req.GetAdditionalAuthenticatedData())}, nil
}

func (k *aeadKMS) Decrypt(_ context.Context, req *kmspb.DecryptRequest) (*kmspb.DecryptResponse, error) {
	gcm, err := k.ensureKey(req.GetName())
	if err != nil {
		return nil, err
	}
	sealed := req.GetCiphertext()
	if len(sealed) < gcm.NonceSize() {
		return nil, status.Error(codes.InvalidArgument, "Decryption failed: the ciphertext is invalid")
	}
	opened, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], req.GetAdditionalAuthenticatedData())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "Decryption failed: the ciphertext is invalid")
	}
	return &kmspb.DecryptResponse{Plaintext: opened}, nil
}

func serveKMS(t *testing.T, kms kmspb.KeyManagementServiceServer) *Clients {
	t.Helper()
	isolated(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	kmspb.RegisterKeyManagementServiceServer(server, kms)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return &Clients{Namespace: "ocel", Project: "acme", Region: "us-central1", Endpoint: "http://" + listener.Addr().String()}
}

func serveRecordingKMS(t *testing.T) (*Clients, *recordingKMS) {
	t.Helper()
	fake := &recordingKMS{}
	return serveKMS(t, fake), fake
}

func TestTheCipherSealsAsEveryCipherMust(t *testing.T) {
	conformance.RunCipher(t, Cipher{Clients: serveKMS(t, &aeadKMS{keys: map[string]cipher.AEAD{}})})
}

var bound = seal.BindToProject("shop", "variable", "production", "staging", "/web", "STRIPE_API_KEY")

func TestAProjectsValueIsSealedUnderItsProjectsKeyAndBoundToItsAssociatedDataBytes(t *testing.T) {
	clients, fake := serveRecordingKMS(t)

	if _, err := (Cipher{Clients: clients}).Seal(context.Background(), environment.TierProduction, bound, []byte("sk_live_secret")); err != nil {
		t.Fatalf("Seal() = %v", err)
	}
	if len(fake.encrypted) != 1 {
		t.Fatalf("KMS was asked to encrypt %d times, want once", len(fake.encrypted))
	}
	if got := fake.encrypted[0].GetName(); got != sealedKey {
		t.Errorf("sealed under %q, want %q", got, sealedKey)
	}
	if got := string(fake.encrypted[0].GetAdditionalAuthenticatedData()); got != boundBytes {
		t.Errorf("bound to %q, want %q: every value already stored is bound to those bytes", got, boundBytes)
	}
}

func TestAProjectsValueSealedUnderItsProjectsKeyAndAssociatedDataBytesOpens(t *testing.T) {
	clients, _ := serveRecordingKMS(t)

	opened, err := (Cipher{Clients: clients}).Open(context.Background(), environment.TierProduction, bound, []byte("sealed"))
	if err != nil || string(opened) != "sk_live_secret" {
		t.Fatalf("Open() = %q, %v, want the value sealed under %s at %q to open", opened, err, sealedKey, boundBytes)
	}
}

func TestACellAndABindingAreSealedUnderTheAssociatedDataBytesEveryStoredValueIsBoundTo(t *testing.T) {
	clients, kms := serveRecordingKMS(t)
	store := variablestore.Store{KeyValues: fake.NewKeyValues(), Cipher: Cipher{Clients: clients}}
	scope := variablestore.Scope{Project: "shop", Tier: environment.TierProduction}

	if _, err := store.Set(context.Background(), scope, variablestore.Coordinate{Cell: variablestore.Cell{Folder: "/web", Key: "STRIPE_API_KEY"}, Environment: "staging"}, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set() = %v", err)
	}
	if _, err := store.SetBinding(context.Background(), scope, "", variablestore.OwnerOcel, "orders", variablestore.BindingWrite{Record: []byte("{}"), Value: []byte("{}")}); err != nil {
		t.Fatalf("SetBinding() = %v", err)
	}

	want := []string{boundBytes, "7:binding,4:shop,10:production,1:*,6:orders,"}
	got := make([]string, 0, len(kms.encrypted))
	for _, req := range kms.encrypted {
		got = append(got, string(req.GetAdditionalAuthenticatedData()))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("bound to %q, want %q: every cell and binding already stored is bound to those bytes", got, want)
	}
}

func TestTwoProjectsOfATierSealUnderKeysOfTheirOwn(t *testing.T) {
	clients, kms := serveRecordingKMS(t)
	blog := seal.BindToProject("blog", "variable", "production", "staging", "/web", "STRIPE_API_KEY")

	for _, each := range []seal.AssociatedData{bound, blog} {
		if _, err := (Cipher{Clients: clients}).Seal(context.Background(), environment.TierProduction, each, []byte("v")); err != nil {
			t.Fatalf("Seal() = %v", err)
		}
	}

	if shop, other := kms.encrypted[0].GetName(), kms.encrypted[1].GetName(); shop == other || shop == tierKey || other == tierKey {
		t.Errorf("shop sealed under %s and blog under %s, want a key of each project's own so an app of one cannot open the other's", shop, other)
	}
}

func TestAValueBoundToNoOneProjectIsSealedUnderTheTierKey(t *testing.T) {
	for name, each := range map[string]seal.AssociatedData{
		"no project":                 seal.Bind("gates"),
		"a project no slug can name": seal.BindToProject("*", "variable", "DIGEST"),
	} {
		t.Run(name, func(t *testing.T) {
			clients, kms := serveRecordingKMS(t)
			if _, err := (Cipher{Clients: clients}).Seal(context.Background(), environment.TierProduction, each, []byte("v")); err != nil {
				t.Fatalf("Seal() = %v", err)
			}
			if got := kms.encrypted[0].GetName(); got != tierKey {
				t.Errorf("sealed under %s, want the tier key %s", got, tierKey)
			}
		})
	}
}
