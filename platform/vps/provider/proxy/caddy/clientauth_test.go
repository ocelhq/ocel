package caddy_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

func clientCertificate(t *testing.T) (string, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "origin pull"},
		DNSNames:     []string{"example.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), der
}

type shieldingPolicy struct {
	Match     map[string][]string `json:"match"`
	Selection map[string][]string `json:"certificate_selection"`
	Client    *struct {
		TrustedLeafCerts []string `json:"trusted_leaf_certs"`
		Mode             string   `json:"mode"`
	} `json:"client_authentication"`
}

func shieldingPolicies(t *testing.T, written []byte) ([]shieldingPolicy, *bool) {
	t.Helper()
	var read struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Policies      []shieldingPolicy `json:"tls_connection_policies"`
					StrictSNIHost *bool             `json:"strict_sni_host"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(written, &read); err != nil {
		t.Fatal(err)
	}
	front := read.Apps.HTTP.Servers["ocel"]
	return front.Policies, front.StrictSNIHost
}

func TestAHostnameShieldedByAClientCertificateIsHandedOnlyToAClientPresentingIt(t *testing.T) {
	t.Parallel()

	certificate, der := clientCertificate(t)
	successor, successorDER := clientCertificate(t)
	spec := specified(pinned("www.example.com", "www"))
	spec.Shields = []proxy.Shield{{Hostname: "Shop.Example.com", ClientCertificates: []string{certificate, successor}}}
	written, _ := render(t, spec)
	policies, strict := shieldingPolicies(t, written)

	var shielding *shieldingPolicy
	for at := range policies[:len(policies)-1] {
		if policies[at].Match["sni"][0] == "shop.example.com" {
			shielding = &policies[at]
		}
	}
	if shielding == nil || shielding.Client == nil {
		t.Fatalf("connection policies %+v, want one for shop.example.com that authenticates the client, ahead of the catch-all", policies)
	}
	want := []string{base64.StdEncoding.EncodeToString(der), base64.StdEncoding.EncodeToString(successorDER)}
	if !slices.Equal(shielding.Client.TrustedLeafCerts, want) {
		t.Errorf("shop.example.com trusts client certificates %v, want the one the edge presents and the successor it presents next", shielding.Client.TrustedLeafCerts)
	}
	if shielding.Client.Mode != "require" {
		t.Errorf("shop.example.com authenticates clients in mode %q, want require: a handshake with no certificate is refused", shielding.Client.Mode)
	}
	if last := policies[len(policies)-1]; last.Client != nil {
		t.Errorf("the catch-all policy authenticates clients %+v, want every hostname nothing shields served to anyone", last.Client)
	}
	if strict == nil || !*strict {
		t.Error("the front server does not hold a request's Host to its handshake's server name, so a client could shake hands for an unshielded name and ask for a shielded one")
	}
}

func TestAPinnedHostnameShieldedByAClientCertificateKeepsItsPin(t *testing.T) {
	t.Parallel()

	certificate, _ := clientCertificate(t)
	spec := specified(pinned("shop.example.com", "shop"))
	spec.Shields = []proxy.Shield{{Hostname: "shop.example.com", ClientCertificates: []string{certificate}}}
	written, _ := render(t, spec)
	policies, _ := shieldingPolicies(t, written)

	if len(policies) != 2 {
		t.Fatalf("connection policies %+v, want the one for shop.example.com and the catch-all", policies)
	}
	shop := policies[0]
	if shop.Client == nil || len(shop.Selection["any_tag"]) != 1 || shop.Selection["any_tag"][0] != caddy.PinsMount+"/shop" {
		t.Errorf("shop.example.com is handed %+v, want its pin and the client authentication in the one policy caddy takes for it", shop)
	}
}

func TestAProxyShieldingNothingLeavesServerNamesAndHostsAsTheyWere(t *testing.T) {
	t.Parallel()

	written, _ := render(t, specified())
	if _, strict := shieldingPolicies(t, written); strict != nil {
		t.Errorf("strict_sni_host = %v on a proxy shielding nothing, want it left out", *strict)
	}
}

func TestAClientCertificateThatIsNoCertificateIsRefusedRatherThanRendered(t *testing.T) {
	t.Parallel()

	spec := specified()
	spec.Shields = []proxy.Shield{{Hostname: "shop.example.com", ClientCertificates: []string{"not a certificate"}}}
	if _, err := (caddy.Builtin{}).Render(spec); err == nil {
		t.Error("Render() = nil, want the unreadable client certificate refused: a proxy that trusts nothing it can read shields nothing")
	}
}

func TestAShieldedHostnameIsAnsweredWithTheOriginCertificateTheEdgeIssuedForIt(t *testing.T) {
	t.Parallel()

	client, _ := clientCertificate(t)
	spec := specified(pinned("shop.example.com", "shop"))
	spec.Shields = []proxy.Shield{{
		Hostname: "shop.example.com", ClientCertificates: []string{client},
		Certificate: "ORIGIN CERTIFICATE", Key: "ORIGIN KEY",
	}}
	written, _ := render(t, spec)

	var read struct {
		Apps struct {
			TLS struct {
				Certificates struct {
					LoadPEM []struct {
						Certificate string   `json:"certificate"`
						Key         string   `json:"key"`
						Tags        []string `json:"tags"`
					} `json:"load_pem"`
				} `json:"certificates"`
			} `json:"tls"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(written, &read); err != nil {
		t.Fatal(err)
	}
	loaded := read.Apps.TLS.Certificates.LoadPEM
	if len(loaded) != 1 || loaded[0].Certificate != "ORIGIN CERTIFICATE" || loaded[0].Key != "ORIGIN KEY" || len(loaded[0].Tags) != 1 {
		t.Fatalf("the proxy loads %+v, want the origin certificate and its key, tagged", loaded)
	}
	policies, _ := shieldingPolicies(t, written)
	if shop := policies[0]; shop.Match["sni"][0] != "shop.example.com" || !slices.Equal(shop.Selection["any_tag"], loaded[0].Tags) {
		t.Errorf("shop.example.com is handed %+v, want the origin certificate the edge trusts rather than the pin or one ordered on demand", shop)
	}
}
