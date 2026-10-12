package gateplugin_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/platform/gcp/gateplugin"
)

func TestAConfigRoundTripsEveryGateItHolds(t *testing.T) {
	t.Parallel()

	gates := map[string]previewgate.Gate{
		"shop": {Key: "k 1,2=%", PasswordMAC: "abc", BypassSecrets: []string{"a b", "-", "c,d"}, AllowOptions: []string{"/api", "/é"}},
		"blog": {Key: "plain"},
	}
	hosts := map[string]string{"pr-1-shop.example.dev": "shop", "pr-2-shop.example.dev": "shop", "main-blog.example.dev": "blog"}

	parsed, err := gateplugin.ParseConfig(gateplugin.EncodeConfig(hosts, gates))
	if err != nil {
		t.Fatalf("ParseConfig = %v", err)
	}

	for host, slug := range hosts {
		got, gated := parsed.FindGate(host)
		if !gated {
			t.Fatalf("FindGate(%s) found no gate", host)
		}
		want := gates[slug]
		if !reflect.DeepEqual(normalized(got), normalized(want)) {
			t.Errorf("FindGate(%s) = %+v, want %+v", host, got, want)
		}
	}
	if _, gated := parsed.FindGate("other.example.dev"); gated {
		t.Error("FindGate found a gate for a host the config does not list")
	}
}

func TestAConfigFindsOneProjectsGateAndTheHostsItGates(t *testing.T) {
	t.Parallel()

	parsed, err := gateplugin.ParseConfig(gateplugin.EncodeConfig(
		map[string]string{"pr-2-shop.example.dev": "shop", "pr-1-shop.example.dev": "shop", "main-blog.example.dev": "blog"},
		map[string]previewgate.Gate{"shop": {Key: "shop-key"}, "blog": {Key: "blog-key"}, "docs": {Key: "docs-key"}},
	))
	if err != nil {
		t.Fatal(err)
	}

	gate, hosts, found := parsed.FindProject("shop")
	if !found || gate.Key != "shop-key" || !reflect.DeepEqual(hosts, []string{"pr-1-shop.example.dev", "pr-2-shop.example.dev"}) {
		t.Errorf("FindProject(shop) = %+v, %v, %t, want shop's gate and its two hosts", gate, hosts, found)
	}
	if gate, hosts, found := parsed.FindProject("docs"); !found || gate.Key != "docs-key" || len(hosts) != 0 {
		t.Errorf("FindProject(docs) = %+v, %v, %t, want docs' gate and no hosts", gate, hosts, found)
	}
	if _, _, found := parsed.FindProject("ghost"); found {
		t.Error("FindProject(ghost) found a gate the config does not hold")
	}
}

func normalized(gate previewgate.Gate) previewgate.Gate {
	if len(gate.BypassSecrets) == 0 {
		gate.BypassSecrets = nil
	}
	if len(gate.AllowOptions) == 0 {
		gate.AllowOptions = nil
	}
	return gate
}

func TestAHostMatchesWhateverCapitalisationTheRequestArrivesIn(t *testing.T) {
	t.Parallel()

	parsed, err := gateplugin.ParseConfig(gateplugin.EncodeConfig(
		map[string]string{"pr-1-shop.example.dev": "shop"},
		map[string]previewgate.Gate{"shop": {Key: "k"}},
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, gated := parsed.FindGate("PR-1-Shop.Example.dev"); !gated {
		t.Error("FindGate missed a host that differs only in capitalisation")
	}
	if _, gated := parsed.FindGate("pr-1-shop.example.dev:443"); !gated {
		t.Error("FindGate missed a host that arrives with its port")
	}
	for _, host := range []string{"pr-1-shop.example.dev.", "PR-1-Shop.Example.dev.:443"} {
		if _, gated := parsed.FindGate(host); !gated {
			t.Errorf("FindGate(%s) missed a host written as a fully qualified name, which reaches the same backend ungated", host)
		}
	}
}

func TestAnEmptyConfigGatesNothing(t *testing.T) {
	t.Parallel()

	parsed, err := gateplugin.ParseConfig(gateplugin.EncodeConfig(nil, nil))
	if err != nil {
		t.Fatalf("ParseConfig of an empty config = %v", err)
	}
	if _, gated := parsed.FindGate("anything.example.dev"); gated {
		t.Error("an empty config gated a host")
	}
}

func TestEncodingTheSameRecordsAlwaysYieldsTheSameText(t *testing.T) {
	t.Parallel()

	hosts := map[string]string{"b.example.dev": "shop", "a.example.dev": "shop", "c.example.dev": "blog"}
	gates := map[string]previewgate.Gate{"shop": {Key: "1"}, "blog": {Key: "2"}}

	first := gateplugin.EncodeConfig(hosts, gates)
	for range 20 {
		if again := gateplugin.EncodeConfig(hosts, gates); again != first {
			t.Fatalf("EncodeConfig changed between calls:\n%s\n%s", first, again)
		}
	}
}

func TestAConfigNamingAHostWithNoGateIsRefusedWhole(t *testing.T) {
	t.Parallel()

	_, err := gateplugin.ParseConfig("ocel-preview-gate 1\nh pr-1.example.dev shop\n")
	if err == nil {
		t.Fatal("ParseConfig accepted a host whose slug has no gate, and that host would pass ungated")
	}
}

func TestATextThatIsNoConfigIsRefused(t *testing.T) {
	t.Parallel()

	for name, text := range map[string]string{
		"empty":           "",
		"a missing title": "h a b\n",
		"another version": "ocel-preview-gate 2\n",
		"an unknown line": "ocel-preview-gate 1\nz what\n",
		"a short gate":    "ocel-preview-gate 1\ng shop key\n",
		"a bad escape":    "ocel-preview-gate 1\ng shop %zz mac - -\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := gateplugin.ParseConfig(text); err == nil {
				t.Errorf("ParseConfig(%q) accepted it", text)
			}
		})
	}
}

func TestAConfigIsPlainLinesSoItStaysSmallAndReadable(t *testing.T) {
	t.Parallel()

	text := gateplugin.EncodeConfig(map[string]string{"a.example.dev": "shop"}, map[string]previewgate.Gate{"shop": {Key: "k"}})
	if strings.ContainsAny(text, "{}\"") {
		t.Errorf("the config carries JSON syntax: %q", text)
	}
}

func TestAConfigErrorNamesTheLineAndFieldButNeverWhatTheyHold(t *testing.T) {
	t.Parallel()

	const secret = "s3cret-gate-key"
	for name, text := range map[string]string{
		"an unknown line":             "ocel-preview-gate 1\nz " + secret + "\n",
		"a short gate":                "ocel-preview-gate 1\ng shop " + secret + "\n",
		"a bad escape in the key":     "ocel-preview-gate 1\ng shop " + secret + "%zz mac - -\n",
		"a truncated bypass secret":   "ocel-preview-gate 1\ng shop key mac " + secret + "% -\n",
		"a bad escape in a host line": "ocel-preview-gate 1\nh " + secret + "%z shop\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := gateplugin.ParseConfig(text)
			if err == nil {
				t.Fatalf("ParseConfig(%q) accepted it", text)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("ParseConfig error %q carries what the config holds, and the plugin logs it", err)
			}
			if !strings.Contains(err.Error(), "line 2") {
				t.Errorf("ParseConfig error %q does not name the line it refused", err)
			}
		})
	}
}
