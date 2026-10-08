package bindingproxy

import (
	"crypto/rand"
	"encoding/hex"
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

func Serve(services Services) (Served, error) {
	token, err := mintToken()
	if err != nil {
		return Served{}, err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Served{}, fmt.Errorf("bind the proxy listener: %w", err)
	}

	srv := &http.Server{Handler: NewMux(token, services)}
	errs := make(chan error, 1)
	go func() { errs <- srv.Serve(ln) }()

	address := "http://" + ln.Addr().String()
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

func mintToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("draw a proxy session token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
