package channel

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
)

func NewSessionToken() string {
	token := make([]byte, 32)
	rand.Read(token)
	return base64.RawURLEncoding.EncodeToString(token)
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
