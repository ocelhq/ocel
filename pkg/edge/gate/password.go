package gate

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/scrypt"
)

const (
	scryptAlgorithm = "scrypt"
	signedAlgorithm = "hmac-sha256"
	hashSeparator   = "$"

	scryptLogN      = 14
	scryptBlockSize = 8
	scryptLanes     = 5
	scryptSaltBytes = 16
	scryptKeyBytes  = 32

	maxRememberedVerdicts = 1024
)

var hashEncoding = base64.RawStdEncoding.Strict()

var derivationSlots = make(chan struct{}, max(1, runtime.GOMAXPROCS(0)/2))

var verdicts = struct {
	sync.Mutex
	matched map[[sha256.Size]byte]bool
}{matched: map[[sha256.Size]byte]bool{}}

type scryptCost struct {
	logN, blockSize, lanes int
}

func (c scryptCost) isWithinBounds() bool {
	return c.logN >= 1 && c.logN <= scryptLogN &&
		c.blockSize >= 1 && c.blockSize <= scryptBlockSize &&
		c.lanes >= 1 && c.lanes <= scryptLanes
}

func HashPassword(password string) string {
	salt := make([]byte, scryptSaltBytes)
	_, _ = rand.Read(salt)
	cost := scryptCost{logN: scryptLogN, blockSize: scryptBlockSize, lanes: scryptLanes}
	return strings.Join([]string{
		scryptAlgorithm,
		strconv.Itoa(cost.logN), strconv.Itoa(cost.blockSize), strconv.Itoa(cost.lanes),
		hashEncoding.EncodeToString(salt), hashEncoding.EncodeToString(deriveScrypt(password, salt, cost)),
	}, hashSeparator)
}

func SignPassword(key, password string) string {
	return signedAlgorithm + hashSeparator + sign(key, passwordPurpose, password)
}

func IsSignedPassword(hash string) bool {
	algorithm, signature, _ := strings.Cut(hash, hashSeparator)
	return algorithm == signedAlgorithm && len(signature) == signatureHexLen && isLowerHex(signature)
}

func (g Gate) HasPassword(password string) bool {
	if !utf8.ValidString(password) {
		return false
	}
	algorithm, rest, _ := strings.Cut(g.PasswordHash, hashSeparator)
	switch algorithm {
	case scryptAlgorithm:
		return hasScryptPassword(rest, password)
	case signedAlgorithm:
		return IsSignedPassword(g.PasswordHash) && hmac.Equal([]byte(sign(g.Key, passwordPurpose, password)), []byte(rest))
	}
	return false
}

func hasScryptPassword(encoded, password string) bool {
	fields := strings.Split(encoded, hashSeparator)
	if len(fields) != 5 {
		return false
	}
	var parameters [3]int
	for i, field := range fields[:3] {
		n, ok := parseCanonicalInt(field)
		if !ok {
			return false
		}
		parameters[i] = n
	}
	cost := scryptCost{logN: parameters[0], blockSize: parameters[1], lanes: parameters[2]}
	if !cost.isWithinBounds() {
		return false
	}
	salt, err := hashEncoding.DecodeString(fields[3])
	if err != nil || len(salt) != scryptSaltBytes {
		return false
	}
	want, err := hashEncoding.DecodeString(fields[4])
	if err != nil || len(want) != scryptKeyBytes {
		return false
	}
	remembered := sha256.Sum256([]byte(encoded + "\x00" + password))
	verdicts.Lock()
	matched, known := verdicts.matched[remembered]
	verdicts.Unlock()
	if known {
		return matched
	}
	derivationSlots <- struct{}{}
	matched = subtle.ConstantTimeCompare(deriveScrypt(password, salt, cost), want) == 1
	<-derivationSlots
	verdicts.Lock()
	if len(verdicts.matched) >= maxRememberedVerdicts {
		clear(verdicts.matched)
	}
	verdicts.matched[remembered] = matched
	verdicts.Unlock()
	return matched
}

func deriveScrypt(password string, salt []byte, cost scryptCost) []byte {
	derived, err := scrypt.Key([]byte(password), salt, 1<<cost.logN, cost.blockSize, cost.lanes, scryptKeyBytes)
	if err != nil {
		return nil
	}
	return derived
}

func parseCanonicalInt(field string) (int, bool) {
	if !isDigits(field) || len(field) > 1 && field[0] == '0' {
		return 0, false
	}
	n, err := strconv.Atoi(field)
	return n, err == nil
}
