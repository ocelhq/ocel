package ports

import (
	"context"
	"net"
	"sync"
	"testing"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/records"
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

func servingKMS(t *testing.T) (*Clients, *recordingKMS) {
	t.Helper()
	isolated(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	fake := &recordingKMS{}
	kmspb.RegisterKeyManagementServiceServer(server, fake)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return &Clients{Namespace: "ocel", Project: "acme", Region: "us-central1", Endpoint: "http://" + listener.Addr().String()}, fake
}

var sealedAt = records.SealScope{Project: "shop", Tier: "production", Env: "staging", Folder: "/web", Name: "STRIPE_API_KEY"}

func TestAValueIsSealedUnderItsTierKeyAndBoundToItsCoordinateBytes(t *testing.T) {
	clients, fake := servingKMS(t)

	if _, err := (Cipher{Clients: clients}).Seal(context.Background(), sealedAt, []byte("sk_live_secret")); err != nil {
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

func TestAValueAlreadySealedUnderItsTierKeyAndCoordinateBytesStillOpens(t *testing.T) {
	clients, _ := servingKMS(t)

	opened, err := (Cipher{Clients: clients}).Open(context.Background(), sealedAt, []byte("sealed"))
	if err != nil || string(opened) != "sk_live_secret" {
		t.Fatalf("Open() = %q, %v, want the value sealed under %s at %q to open", opened, err, sealedKey, boundBytes)
	}
}
