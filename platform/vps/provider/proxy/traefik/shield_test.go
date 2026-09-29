package traefik_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/traefik"
)

func TestAHostnameAnEdgeForwardsIsRefusedBehindYourTraefikWhoseRoutersRequireNoClientCertificate(t *testing.T) {
	t.Parallel()

	front := traefik.Traefik{Directory: "/etc/traefik/dynamic"}
	err := front.RefuseUnshielded(context.Background(), "shop.example.com")
	var rejection refusal.Refusal
	if !errors.As(err, &rejection) || rejection.Code != refusal.CodeInvalid ||
		!strings.Contains(err.Error(), "shop.example.com") || !strings.Contains(err.Error(), traefik.FileName) {
		t.Errorf("RefuseUnshielded() = %v, want it refused as invalid, naming the hostname and %s", err, traefik.FileName)
	}
	if files, err := front.OriginFiles(proxy.Spec{Hostnames: []string{"shop.example.com"}}); err != nil || len(files) != 0 {
		t.Errorf("OriginFiles() = %v, %v; want none placed beside %s", files, err, traefik.FileName)
	}
}
