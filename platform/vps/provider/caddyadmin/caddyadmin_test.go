package caddyadmin

import (
	"strings"
	"testing"
)

const socket = "/run/caddy-admin.sock"

func TestTheAdminAddressNamesTheModeTheSocketIsCreatedUnder(t *testing.T) {
	t.Parallel()

	listen := Listen(socket)
	if !strings.HasPrefix(listen, "unix/"+socket+"|") {
		t.Fatalf("the admin endpoint is declared as %q, want the unix socket at %s carrying a mode", listen, socket)
	}
	if !strings.HasSuffix(listen, "|0600") {
		t.Errorf("the admin endpoint is declared as %q: a socket left at the proxy's own default is one whose access control nothing in this repository decided", listen)
	}
}

func TestAConfigThatWouldLeaveTheSocketsModeToTheProxyIsRefused(t *testing.T) {
	t.Parallel()

	for what, document := range map[string]string{
		"an endpoint moved to a port a peer can dial": `{"admin":{"listen":"tcp/:2019"}}`,
		"an endpoint switched off entirely":           `{"admin":{"disabled":true,"listen":"unix/` + socket + `|0600"}}`,
		"no admin block at all":                       `{"apps":{}}`,
		"the socket named without its mode":           `{"admin":{"listen":"unix/` + socket + `"}}`,
		"the socket named under a wider mode":         `{"admin":{"listen":"unix/` + socket + `|0666"}}`,
	} {
		if err := Keeps([]byte(document), socket); err == nil {
			t.Errorf("%s is accepted, and caddy applies the admin section before it validates the rest: %s", what, document)
		}
	}
	if err := Keeps([]byte(`{"admin":{"listen":"`+Listen(socket)+`"}}`), socket); err != nil {
		t.Errorf("the address this package itself declares is refused: %v", err)
	}
}
