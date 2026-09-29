package caddyfile_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddyfile"
)

func TestAHostnameAnEdgeForwardsIsRefusedBehindYourCaddyWhichRequiresNoClientCertificate(t *testing.T) {
	t.Parallel()

	front := caddyfile.Caddyfile{Box: &box{}, Container: "caddy"}
	err := front.RefuseUnshielded(context.Background(), "shop.example.com")
	var rejection refusal.Refusal
	if !errors.As(err, &rejection) || rejection.Code != refusal.CodeInvalid ||
		!strings.Contains(err.Error(), "shop.example.com") || !strings.Contains(err.Error(), caddyfile.FileName) {
		t.Errorf("RefuseUnshielded() = %v, want it refused as invalid, naming the hostname and %s", err, caddyfile.FileName)
	}
}
