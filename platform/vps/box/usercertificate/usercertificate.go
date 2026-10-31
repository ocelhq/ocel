package usercertificate

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

const (
	Lifetime = 10 * time.Minute
	Backdate = 30 * time.Second
	skew     = time.Minute
)

var keyIDShape = regexp.MustCompile(`^ocel-[\w+=,.@-]{1,12} [A-Za-z0-9_-]{1,64} (production|preview)$`)

func Sign(key []byte, principal string, request boxapi.CertificateRequest, now time.Time) (string, error) {
	if principal == "" {
		return "", errors.New("the box's config names no principal")
	}
	if request.Principal != principal {
		return "", fmt.Errorf("the certificate asked for names %q, and this box signs only for %q", request.Principal, principal)
	}
	if !keyIDShape.MatchString(request.KeyID) {
		return "", fmt.Errorf("the key id %q is not `<session> <project> <tier>`", request.KeyID)
	}
	after, before := time.Unix(request.ValidAfter, 0), time.Unix(request.ValidBefore, 0)
	switch {
	case !before.After(after):
		return "", errors.New("the certificate asked for ends before it starts")
	case before.Sub(after) > Lifetime:
		return "", fmt.Errorf("the certificate asked for lasts %s, longer than %s", before.Sub(after), Lifetime)
	case after.Before(now.Add(-Backdate - skew)):
		return "", errors.New("the certificate asked for starts in the past")
	case before.After(now.Add(Lifetime + skew)):
		return "", fmt.Errorf("the certificate asked for ends more than %s from now", Lifetime)
	}
	public, _, _, _, err := ssh.ParseAuthorizedKey([]byte(request.PublicKey))
	if err != nil {
		return "", fmt.Errorf("read the public key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("read the signing key: %w", err)
	}
	serial := make([]byte, 8)
	if _, err := rand.Read(serial); err != nil {
		return "", err
	}
	certificate := &ssh.Certificate{
		Key:             public,
		Serial:          binary.BigEndian.Uint64(serial),
		CertType:        ssh.UserCert,
		KeyId:           request.KeyID,
		ValidPrincipals: []string{principal},
		ValidAfter:      uint64(request.ValidAfter),
		ValidBefore:     uint64(request.ValidBefore),
		Permissions: ssh.Permissions{Extensions: map[string]string{
			"permit-port-forwarding": "",
			"permit-pty":             "",
		}},
	}
	if err := certificate.SignCert(rand.Reader, signer); err != nil {
		return "", fmt.Errorf("sign the certificate: %w", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(certificate))), nil
}
