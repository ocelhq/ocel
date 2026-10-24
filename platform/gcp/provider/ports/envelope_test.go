package ports

import (
	"context"
	"crypto/cipher"
	"fmt"
	"sync/atomic"
	"testing"

	"cloud.google.com/go/kms/apiv1/kmspb"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/seal"
)

type countingKMS struct {
	*aeadKMS

	encrypts atomic.Int32
	decrypts atomic.Int32
}

func (k *countingKMS) Encrypt(ctx context.Context, req *kmspb.EncryptRequest) (*kmspb.EncryptResponse, error) {
	k.encrypts.Add(1)
	return k.aeadKMS.Encrypt(ctx, req)
}

func (k *countingKMS) Decrypt(ctx context.Context, req *kmspb.DecryptRequest) (*kmspb.DecryptResponse, error) {
	k.decrypts.Add(1)
	return k.aeadKMS.Decrypt(ctx, req)
}

func TestTheEnvelopeSealsAsEveryCipherMust(t *testing.T) {
	conformance.RunCipher(t, Envelope{Clients: serveKMS(t, &aeadKMS{keys: map[string]cipher.AEAD{}})})
}

func TestAnEnvelopeAsksCloudKMSOnceToSealManyValuesAndOnceAgainToOpenThemElsewhere(t *testing.T) {
	ctx := context.Background()
	kms := &countingKMS{aeadKMS: &aeadKMS{keys: map[string]cipher.AEAD{}}}
	sealing := serveKMS(t, kms)
	tier := environment.TierProduction

	var sealed [][]byte
	for i := range 3 {
		value, err := Envelope{Clients: sealing}.Seal(ctx, tier, seal.BindToProject("shop", "run payload", fmt.Sprint(i)), []byte("payload"))
		if err != nil {
			t.Fatal(err)
		}
		sealed = append(sealed, value)
	}
	fresh := &Clients{Namespace: sealing.Namespace, Project: sealing.Project, Region: sealing.Region, Endpoint: sealing.Endpoint}
	for i, value := range sealed {
		opened, err := Envelope{Clients: fresh}.Open(ctx, tier, seal.BindToProject("shop", "run payload", fmt.Sprint(i)), value)
		if err != nil || string(opened) != "payload" {
			t.Fatalf("Open(%d) = %q, %v, want the payload", i, opened, err)
		}
	}

	if got := kms.encrypts.Load(); got != 1 {
		t.Errorf("sealing three values asked Cloud KMS %d times, want once, for the data key", got)
	}
	if got := kms.decrypts.Load(); got != 1 {
		t.Errorf("opening three values in another process asked Cloud KMS %d times, want once, for the data key", got)
	}
}
