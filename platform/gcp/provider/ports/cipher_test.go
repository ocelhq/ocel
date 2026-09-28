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
	"github.com/ocelhq/ocel/pkg/envvars"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/seal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	boundBytes = "shop/production/staging/%2Fweb//STRIPE_API_KEY/"
	sealedKey  = "projects/acme/locations/us-central1/keyRings/ocel/cryptoKeys/production"
)

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

func TestCipherConformance(t *testing.T) {
	conformance.RunCipher(t, Cipher{Clients: serveKMS(t, &aeadKMS{keys: map[string]cipher.AEAD{}})})
}

var bound = seal.AssociatedData{
	{Name: "project", Value: "shop"},
	{Name: "class", Value: "production"},
	{Name: "environment", Value: "staging"},
	{Name: "folder", Value: "/web"},
	{Name: "binding", Value: ""},
	{Name: "key", Value: "STRIPE_API_KEY"},
}

func TestAValueIsSealedUnderItsTierKeyAndBoundToItsAssociatedDataBytes(t *testing.T) {
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

func TestAValueSealedUnderItsTierKeyAndAssociatedDataBytesOpens(t *testing.T) {
	clients, _ := serveRecordingKMS(t)

	opened, err := (Cipher{Clients: clients}).Open(context.Background(), environment.TierProduction, bound, []byte("sealed"))
	if err != nil || string(opened) != "sk_live_secret" {
		t.Fatalf("Open() = %q, %v, want the value sealed under %s at %q to open", opened, err, sealedKey, boundBytes)
	}
}

func TestACellAndABindingAreSealedUnderTheAssociatedDataBytesEveryStoredValueIsBoundTo(t *testing.T) {
	clients, kms := serveRecordingKMS(t)
	store := envvars.Store{Records: fake.NewRecords(), Cipher: Cipher{Clients: clients}}
	scope := envvars.Scope{Project: "shop", Tier: environment.TierProduction}

	if _, err := store.Set(context.Background(), scope, envvars.Coordinate{Cell: envvars.Cell{Folder: "/web", Key: "STRIPE_API_KEY"}, Environment: "staging"}, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set() = %v", err)
	}
	if _, err := store.SetBinding(context.Background(), scope, "", envvars.OwnerOcel, "orders", envvars.BindingWrite{Record: []byte("{}"), Value: []byte("{}")}); err != nil {
		t.Fatalf("SetBinding() = %v", err)
	}

	want := []string{boundBytes, "shop/production/*/%2F/orders/PROPERTIES/"}
	got := make([]string, 0, len(kms.encrypted))
	for _, req := range kms.encrypted {
		got = append(got, string(req.GetAdditionalAuthenticatedData()))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("bound to %q, want %q: every cell and binding already stored is bound to those bytes", got, want)
	}
}
