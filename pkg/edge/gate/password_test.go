package gate_test

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/scrypt"

	"github.com/ocelhq/ocel/pkg/edge/gate"
)

func scryptHash(t *testing.T, password string, logN, r, p int) string {
	t.Helper()
	salt := []byte("sixteen byte slt")
	derived, err := scrypt.Key([]byte(password), salt, 1<<logN, r, p, 32)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join([]string{
		"scrypt", strconv.Itoa(logN), strconv.Itoa(r), strconv.Itoa(p),
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(derived),
	}, "$")
}

func TestAPasswordHashNamesScryptAtTheOWASPCostWithAFreshSalt(t *testing.T) {
	t.Parallel()

	first, second := gate.HashPassword("correct horse battery"), gate.HashPassword("correct horse battery")

	fields := strings.Split(first, "$")
	if len(fields) != 6 || fields[0] != "scrypt" || fields[1] != "14" || fields[2] != "8" || fields[3] != "5" {
		t.Fatalf("HashPassword = %q, want scrypt$14$8$5$<salt>$<key>", first)
	}
	if salt, err := base64.RawStdEncoding.DecodeString(fields[4]); err != nil || len(salt) != 16 {
		t.Errorf("the salt %q decodes to %d bytes (%v), want 16", fields[4], len(salt), err)
	}
	if derived, err := base64.RawStdEncoding.DecodeString(fields[5]); err != nil || len(derived) != 32 {
		t.Errorf("the derived key %q decodes to %d bytes (%v), want 32", fields[5], len(derived), err)
	}
	if first == second {
		t.Errorf("two hashes of one password are both %q, want each made with its own salt", first)
	}
}

func TestAGateHasThePasswordItsHashWasMadeFromAndNoOther(t *testing.T) {
	t.Parallel()

	held := gate.Gate{Key: "k", PasswordHash: gate.HashPassword("correct horse battery")}

	if !held.HasPassword("correct horse battery") {
		t.Error("the gate refused the password its hash was made from")
	}
	for _, other := range []string{"correct horse batterz", "", "correct horse battery "} {
		if held.HasPassword(other) {
			t.Errorf("the gate accepted %q, which its hash was not made from", other)
		}
	}
}

func TestAGateHasThePasswordOfAnScryptHashMadeElsewhereFromTheSameParameters(t *testing.T) {
	t.Parallel()

	held := gate.Gate{Key: "k", PasswordHash: scryptHash(t, "pw", 14, 8, 5)}

	if !held.HasPassword("pw") {
		t.Errorf("the gate refused the password of %q", held.PasswordHash)
	}
}

func TestAGateRefusesAnScryptHashCostlierThanItsBoundsEvenWhenThePasswordMatches(t *testing.T) {
	t.Parallel()

	for _, cost := range [][3]int{{15, 8, 1}, {14, 9, 1}, {13, 8, 6}} {
		held := gate.Gate{Key: "k", PasswordHash: scryptHash(t, "pw", cost[0], cost[1], cost[2])}
		if held.HasPassword("pw") {
			t.Errorf("the gate derived %q, which costs more than scrypt$14$8$5", held.PasswordHash)
		}
	}
}

func TestAGateRefusesAPasswordHashItCannotParse(t *testing.T) {
	t.Parallel()

	valid := scryptHash(t, "pw", 14, 8, 5)
	fields := strings.Split(valid, "$")
	with := func(index int, value string) string {
		changed := append([]string(nil), fields...)
		changed[index] = value
		return strings.Join(changed, "$")
	}
	for name, hash := range map[string]string{
		"empty":                     "",
		"no parameters":             "scrypt",
		"an unknown algorithm":      with(0, "bcrypt"),
		"an upper-case algorithm":   with(0, "SCRYPT"),
		"a padded cost":             with(1, "014"),
		"a signed cost":             with(1, "+14"),
		"a zero cost":               with(1, "0"),
		"a zero block size":         with(2, "0"),
		"a zero parallelism":        with(3, "0"),
		"a padded salt":             with(4, fields[4]+"=="),
		"a short salt":              with(4, fields[4][:20]),
		"a non-canonical salt":      with(4, fields[4][:21]+"B"),
		"a short derived key":       with(5, fields[5][:40]),
		"a padded derived key":      with(5, fields[5]+"="),
		"an extra field":            valid + "$x",
		"a signature with no value": "hmac-sha256$",
		"a signature in upper case": "hmac-sha256$" + strings.ToUpper(strings.TrimPrefix(gate.SignPassword("k", "pw"), "hmac-sha256$")),
	} {
		if (gate.Gate{Key: "k", PasswordHash: hash}).HasPassword("pw") {
			t.Errorf("%s: the gate accepted a password against %q", name, hash)
		}
	}
}

func TestASignedPasswordIsTheHMACOfThePasswordUnderTheGateKey(t *testing.T) {
	t.Parallel()

	const want = "hmac-sha256$f18e0cd484c475051052b9fed4f125e1db487c694fc74b1128b40bb4af41e5a3"
	if got := gate.SignPassword("test-preview-key", "JBSWY3DPEHPK3PXP2345"); got != want {
		t.Errorf("SignPassword = %q, want %q", got, want)
	}
	signed := gate.Gate{Key: "test-preview-key", PasswordHash: want}
	if !signed.HasPassword("JBSWY3DPEHPK3PXP2345") {
		t.Error("the gate refused the password it signed")
	}
	if rekeyed := (gate.Gate{Key: "another-key", PasswordHash: want}); rekeyed.HasPassword("JBSWY3DPEHPK3PXP2345") {
		t.Error("a gate under another key accepted the password signed under the first")
	}
}

func TestAGateRefusesAPasswordThatIsNotUTF8(t *testing.T) {
	t.Parallel()

	for _, held := range []gate.Gate{
		{Key: "k", PasswordHash: gate.HashPassword("�")},
		{Key: "k", PasswordHash: gate.SignPassword("k", "�")},
	} {
		if held.HasPassword("\xff") {
			t.Errorf("the gate holding %q accepted a password that is not UTF-8", held.PasswordHash)
		}
	}
}

func TestOnlyASignedPasswordIsASignedPassword(t *testing.T) {
	t.Parallel()

	if signed := gate.SignPassword("k", "pw"); !gate.IsSignedPassword(signed) {
		t.Errorf("IsSignedPassword(%q) = false, want true", signed)
	}
	for _, hash := range []string{gate.HashPassword("pw"), "", "hmac-sha256$", "hmac-sha256$" + strings.Repeat("g", 64)} {
		if gate.IsSignedPassword(hash) {
			t.Errorf("IsSignedPassword(%q) = true, want false", hash)
		}
	}
}

func TestAGateDerivesAPasswordOnceHoweverOftenItIsChecked(t *testing.T) {
	t.Parallel()

	held := gate.Gate{Key: "k", PasswordHash: gate.HashPassword("checked often")}
	started := time.Now()
	held.HasPassword("checked often")
	held.HasPassword("not it")
	derivations := time.Since(started)

	started = time.Now()
	for range 20 {
		if !held.HasPassword("checked often") || held.HasPassword("not it") {
			t.Fatal("a repeated check changed its answer")
		}
	}
	if repeated := time.Since(started); repeated >= derivations {
		t.Errorf("40 repeated checks took %v, the two that derived took %v, want the repeats answered without deriving", repeated, derivations)
	}
}
