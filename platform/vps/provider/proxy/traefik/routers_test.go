package traefik_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/traefik"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	coolifyDynamic = "/data/coolify/proxy/dynamic"
	dokployDynamic = "/etc/dokploy/traefik/dynamic"
)

func coolifyBox(t *testing.T) *box {
	return &box{beside: besideIn(t, "coolify"), containers: fixture(t, "coolify/containers.txt")}
}

func dokployBox(t *testing.T) *box {
	return &box{
		beside:     besideIn(t, "dokploy"),
		containers: fixture(t, "dokploy/containers.txt"),
		services:   fixture(t, "dokploy/services.txt"),
	}
}

func coolifys(machine *box) traefik.Traefik {
	return traefik.Traefik{Box: machine, Directory: coolifyDynamic, Resolver: "letsencrypt", HTTP: "http", HTTPS: "https", Network: "coolify"}
}

func dokploys(machine *box) traefik.Traefik {
	return traefik.Traefik{Box: machine, Directory: dokployDynamic, Resolver: "letsencrypt", HTTP: "web", HTTPS: "websecure", Network: "dokploy-network"}
}

func refusedNaming(t *testing.T, err error, mentions ...string) {
	t.Helper()
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Fatalf("RefuseRouted() = %v, want a busy refusal", err)
	}
	for _, mention := range mentions {
		if !strings.Contains(err.Error(), mention) {
			t.Errorf("RefuseRouted() = %v, want it to name %s", err, mention)
		}
	}
}

func TestAHostnameACoolifyAppRoutesByItsContainerLabelsIsRefused(t *testing.T) {
	t.Parallel()

	err := coolifys(coolifyBox(t)).RefuseRouted(context.Background(), []string{"web.p1305.test"})
	refusedNaming(t, err, "web.p1305.test", "https-0-z1jkcokc10dwzdk8i5huimhi", "container z1jkcokc10dwzdk8i5huimhi-122346088210")
}

func TestAHostnameCoolifysOwnFileRoutesIsRefusedNamingTheFile(t *testing.T) {
	t.Parallel()

	err := coolifys(coolifyBox(t)).RefuseRouted(context.Background(), []string{"COOLIFY.p1305.test"})
	refusedNaming(t, err, "coolify-https", coolifyDynamic+"/coolify.yaml")
}

func TestAHostnameADokployApplicationRoutesByItsFileIsRefusedNamingTheFile(t *testing.T) {
	t.Parallel()

	err := dokploys(dokployBox(t)).RefuseRouted(context.Background(), []string{"dapp.p1305.test"})
	refusedNaming(t, err, "dapp-lekbai-router-websecure-1", dokployDynamic+"/dapp-lekbai.yml")
}

func TestAHostnameASwarmServiceRoutesByItsLabelsIsRefused(t *testing.T) {
	t.Parallel()

	err := dokploys(dokployBox(t)).RefuseRouted(context.Background(), []string{"stack.p1305.test"})
	refusedNaming(t, err, "stack-p1306-7ktq2a-1-websecure", "swarm service stack-p1306-7ktq2a_web")
}

func TestAHostnameNothingOfYoursRoutesIsLetThroughPastEveryCatchAll(t *testing.T) {
	t.Parallel()

	for tool, front := range map[string]traefik.Traefik{"Coolify": coolifys(coolifyBox(t)), "Dokploy": dokploys(dokployBox(t))} {
		if err := front.RefuseRouted(context.Background(), []string{"shop.p1305.test", "ocel-edge-probe.preview.p1305.test"}); err != nil {
			t.Errorf("RefuseRouted() on %s's box = %v, want nothing refused: its catch-all takes what nobody routes, which is what ocel is claiming", tool, err)
		}
	}
}

func TestAHostRegexpThatMatchesTheHostnameRefusesItAndOneThatMatchesAnythingDoesNot(t *testing.T) {
	t.Parallel()

	machine := &box{beside: []switchboard.SiblingFile{{Name: "apps.toml", Content: []byte(`
[http.routers.tenants]
rule = "HostRegexp(` + "`" + `^[a-z]+\\.example\\.com$` + "`" + `)"
service = "tenants"

[http.routers.everything]
rule = "HostRegexp(` + "`" + `.+` + "`" + `)"
service = "fallback"
`)}}}
	front := traefik.Traefik{Box: machine, Directory: "/etc/traefik/dynamic", Resolver: "le", HTTP: "web", HTTPS: "websecure", Port: 8480}
	refusedNaming(t, front.RefuseRouted(context.Background(), []string{"shop.example.com"}), "tenants", "/etc/traefik/dynamic/apps.toml")
	if err := front.RefuseRouted(context.Background(), []string{"shop.example.org"}); err != nil {
		t.Errorf("RefuseRouted(shop.example.org) = %v, want only the catch-all to match it, and a catch-all claims no hostname", err)
	}
}

func TestAHostnameAFileOfYoursRoutesThroughTraefiksTemplateFunctionsIsRefused(t *testing.T) {
	t.Parallel()

	for name, rule := range map[string]string{
		"sprig.yml":     "Host(`{{ \"SHOP.example.com\" | lower }}`)",
		"split.yml":     "Host(`{{ index (split \"shop.example.com\" \".\") 0 }}.example.com`)",
		"normalize.yml": "Host(`{{ normalize \"shop\" }}.example.com`)",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			machine := &box{beside: []switchboard.SiblingFile{{Name: name, Content: []byte("http:\n  routers:\n    shop:\n      rule: " + rule + "\n      service: shop\n")}}}
			front := traefik.Traefik{Box: machine, Directory: "/etc/traefik/dynamic", Resolver: "le", HTTP: "web", HTTPS: "websecure", Port: 8480}
			refusedNaming(t, front.RefuseRouted(context.Background(), []string{"shop.example.com"}), "router shop", "/etc/traefik/dynamic/"+name)
		})
	}
}

func TestAClaimIsRefusedWhileAFileOfYoursCannotBeReadForItsRouters(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"broken.yml":   "http:\n  routers:\n    web:\n      rule: Host(`a.example.com`)\n     service: web\n",
		"broken.toml":  "[http.routers.web\nrule = 1\n",
		"unknown.yml":  "http:\n  routers:\n    web:\n      rule: Host(`{{ nosuch }}`)\n",
		"unparsed.yml": "http:\n  routers:\n    {{ if }}\n",
		"env.yml":      "http:\n  routers:\n    web:\n      rule: Host(`{{ env \"SHOP\" }}`)\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			machine := &box{beside: []switchboard.SiblingFile{{Name: name, Content: []byte(content)}}}
			front := traefik.Traefik{Box: machine, Directory: "/etc/traefik/dynamic", Resolver: "le", HTTP: "web", HTTPS: "websecure", Port: 8480}
			err := front.RefuseRouted(context.Background(), []string{"shop.example.com"})
			var refused refusal.Refusal
			if !errors.As(err, &refused) || !strings.Contains(err.Error(), "/etc/traefik/dynamic/"+name) {
				t.Errorf("RefuseRouted() = %v, want the claim refused naming the file: its routers may already take the hostname", err)
			}
		})
	}
}

func TestAContainerYourTraefikIsToldToIgnoreRoutesNothing(t *testing.T) {
	t.Parallel()

	machine := &box{containers: `"/parked" {"traefik.enable":"false","traefik.http.routers.parked.rule":"Host(` + "`shop.example.com`" + `)"}` + "\n"}
	front := traefik.Traefik{Box: machine, Directory: "/etc/traefik/dynamic", Resolver: "le", HTTP: "web", HTTPS: "websecure", Port: 8480}
	if err := front.RefuseRouted(context.Background(), []string{"shop.example.com"}); err != nil {
		t.Errorf("RefuseRouted() = %v, want a container labelled traefik.enable=false passed over", err)
	}
}
