package lease_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/lease"
)

func TestEveryLeaseTokenMintedIsNewAndUnguessablyLong(t *testing.T) {
	t.Parallel()

	first, err := lease.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := lease.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 32 {
		t.Errorf("NewToken() minted %q then %q, want two different 32-character tokens", first, second)
	}
}
