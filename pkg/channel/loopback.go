package channel

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
)

func NewSessionToken() (string, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return "", fmt.Errorf("channel: generate a session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(token), nil
}

func LoopbackGuard(address, token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != address {
			http.Error(w, "this request names a host other than the server's own; a name that resolves here later is not this server", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+address {
			http.Error(w, "this request comes from another origin", http.StatusForbidden)
			return
		}
		if !VerifyAuthHeader(r.Header.Get("Authorization"), token) {
			http.Error(w, "this request carries no valid session token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
