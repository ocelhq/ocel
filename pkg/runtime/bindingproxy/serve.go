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

type Served struct {
	Address string
	Token   string
	Env     []string
	Errs    <-chan error
	server  *http.Server
}

func (s Served) Close() error {
	if s.server == nil {
		return nil
	}
	return s.server.Close()
}

type Grant struct {
	Grantee  string
	Services Services
}

type Session struct {
	Grantee string
	Token   string
}

type ServedGrants struct {
	Address  string
	Sessions []Session
	server   *http.Server
	watched  <-chan struct{}
}

func (s ServedGrants) Close() error {
	err := s.server.Close()
	<-s.watched
	return err
}

func ServeGrants(grants []Grant, report func(error)) (ServedGrants, error) {
	sessions := make([]Session, 0, len(grants))
	muxes := make([]tokenMux, 0, len(grants))
	for _, grant := range grants {
		token, err := mintToken()
		if err != nil {
			return ServedGrants{}, err
		}
		sessions = append(sessions, Session{Grantee: grant.Grantee, Token: token})
		muxes = append(muxes, tokenMux{token: token, mux: NewMux(token, grant.Services)})
	}
	srv, address, errs, err := listen(&router{muxes: muxes})
	if err != nil {
		return ServedGrants{}, err
	}
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		if err := <-errs; err != nil && !errors.Is(err, http.ErrServerClosed) {
			report(err)
		}
	}()
	return ServedGrants{Address: address, Sessions: sessions, server: srv, watched: watched}, nil
}

func Serve(services Services) (Served, error) {
	token, err := mintToken()
	if err != nil {
		return Served{}, err
	}

	srv, address, errs, err := listen(NewMux(token, services))
	if err != nil {
		return Served{}, err
	}

	return Served{
		Address: address,
		Token:   token,
		Env: []string{
			processenv.RuntimeAddressEnvVar + "=" + address,
			localrpc.SessionTokenEnvVar + "=" + token,
		},
		Errs:   errs,
		server: srv,
	}, nil
}

func listen(handler http.Handler) (*http.Server, string, <-chan error, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", nil, fmt.Errorf("bind the proxy listener: %w", err)
	}
	srv := &http.Server{Handler: handler}
	errs := make(chan error, 1)
	go func() { errs <- srv.Serve(ln) }()
	return srv, "http://" + ln.Addr().String(), errs, nil
}

func mintToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("draw a proxy session token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
