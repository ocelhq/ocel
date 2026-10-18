package bindingproxy

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/processenv"
)

type Grant struct {
	Grantee  string
	Services Services
}

type Session struct {
	Grantee string
	Token   string
}

type Served struct {
	Address  string
	Sessions []Session
	server   *http.Server
	watched  <-chan struct{}
}

func (s Served) Env(grantee string) []string {
	for _, session := range s.Sessions {
		if session.Grantee == grantee {
			return []string{
				processenv.RuntimeAddressEnvVar + "=" + s.Address,
				localrpc.SessionTokenEnvVar + "=" + session.Token,
			}
		}
	}
	return nil
}

func (s Served) Close() error {
	if s.server == nil {
		return nil
	}
	err := s.server.Close()
	<-s.watched
	return err
}

func Serve(grants []Grant, report func(error)) (Served, error) {
	sessions := make([]Session, 0, len(grants))
	muxes := make([]tokenMux, 0, len(grants))
	for _, grant := range grants {
		token, err := mintToken()
		if err != nil {
			return Served{}, err
		}
		sessions = append(sessions, Session{Grantee: grant.Grantee, Token: token})
		muxes = append(muxes, tokenMux{token: token, mux: NewMux(grant.Services)})
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Served{}, fmt.Errorf("bind the proxy listener: %w", err)
	}
	srv := &http.Server{Handler: &router{muxes: muxes}}
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			report(err)
		}
	}()
	return Served{Address: "http://" + ln.Addr().String(), Sessions: sessions, server: srv, watched: watched}, nil
}

func mintToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("draw a proxy session token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
