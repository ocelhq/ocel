package edge

import (
	"strings"
	"testing"
)

const testPreviewKey PreviewKey = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"

func TestAPreviewHostSplitsItsLabelIntoThePrefixAndTheSignedTail(t *testing.T) {
	t.Parallel()

	host := PreviewHost{Hostname: testPreviewKey.Sign("pr-12-web", "abcdefghijklmnop") + ".preview.acme.com"}
	if got := host.ReadPrefix(); got != "pr-12-web" {
		t.Errorf("Prefix() = %q, want pr-12-web", got)
	}
	if got := host.ReadTail(); got != "abcdefghijklmnopp3347l26" {
		t.Errorf("Tail() = %q, want the token and its MAC", got)
	}
	for _, hostname := range []string{"abcdefghijklmnopp3347l26.preview.acme.com", "-abcdefghijklmnopp3347l26.preview.acme.com", "pr-12.preview.acme.com", "prxabcdefghijklmnopp3347l26.preview.acme.com"} {
		if prefix, tail := (PreviewHost{Hostname: hostname}).ReadPrefix(), (PreviewHost{Hostname: hostname}).ReadTail(); prefix != "" || tail != "" {
			t.Errorf("%s splits into %q and %q, want neither: it is no signed preview label", hostname, prefix, tail)
		}
	}
}

func TestSharedPreviewNamesTheProjectAndOneTokenPerApp(t *testing.T) {
	t.Parallel()

	site := NewSharedPreviewSite("shop", "preview.acme.com", testPreviewKey)
	single := site.ListHosts("pr-12", "abcdefghijklmnop", []string{"web"})
	if len(single) != 1 || single[0] != (PreviewHost{Hostname: "shop-abcdefghijklmnoproheemcq.preview.acme.com", App: "web"}) {
		t.Fatalf("Hosts = %+v, want the slug and the token alone: the shared wildcard finds the project by everything before the last dash", single)
	}

	multi := site.ListHosts("pr-12", "abcdefghijklmnop", []string{"web", "api"})
	if len(multi) != 2 || multi[0].App != "web" || multi[1].App != "api" {
		t.Fatalf("Hosts = %+v, want one hostname per app", multi)
	}
	if multi[0].Hostname == multi[1].Hostname {
		t.Errorf("both apps are served on %s, want each its own hostname", multi[0].Hostname)
	}
	for _, host := range multi {
		label := host.ReadLabel()
		if !strings.HasPrefix(label, "shop-") || len(label) != len("shop-")+PreviewTailLen || !isSignedBy(testPreviewKey, label) {
			t.Errorf("%s's label %q, want shop- and a signed token", host.App, label)
		}
		if strings.Contains(label, "pr-12") {
			t.Errorf("%s's label %q names the preview, want it never in the URL", host.App, label)
		}
	}
}

func TestProjectPreviewNamesThePreviewAndAppInFrontOfTheToken(t *testing.T) {
	t.Parallel()

	site := NewProjectPreviewSite("preview.acme.com", testPreviewKey)
	if got := site.ListHosts("pr-12", "abcdefghijklmnop", nil); len(got) != 1 || !strings.HasPrefix(got[0].Hostname, "pr-12-abcdefghijklmnop") {
		t.Errorf("Hosts = %+v, want the preview's name in front of its token", got)
	}
	got := site.ListHosts("pr-12", "abcdefghijklmnop", []string{"web", "api"})
	if len(got) != 2 || !strings.HasPrefix(got[0].Hostname, "pr-12-web-") || !strings.HasPrefix(got[1].Hostname, "pr-12-api-") {
		t.Errorf("Hosts = %+v, want each app named after the preview", got)
	}
	if strings.TrimPrefix(got[0].ReadLabel(), "pr-12-web-")[:PreviewTokenLen] == strings.TrimPrefix(got[1].ReadLabel(), "pr-12-api-")[:PreviewTokenLen] {
		t.Errorf("Hosts = %+v, want each app its own token", got)
	}
	for _, host := range got {
		if !isSignedBy(testPreviewKey, host.ReadLabel()) {
			t.Errorf("%s does not verify under the key that signed it", host.Hostname)
		}
	}
}

func TestProjectPreviewShortensALongNameToFitOneDNSLabel(t *testing.T) {
	t.Parallel()

	name := strings.Repeat("feature-", 7) + "login-3fa2b1c9"
	site := NewProjectPreviewSite("preview.acme.com", testPreviewKey)
	for _, host := range site.ListHosts(name, "abcdefghijklmnop", []string{"web", "dashboard"}) {
		label := host.ReadLabel()
		if len(label) > PreviewLabelMaxLen {
			t.Errorf("%s's label is %d characters, want at most %d", host.App, len(label), PreviewLabelMaxLen)
		}
		if !strings.Contains(label, "-"+host.App+"-") || !strings.HasPrefix(label, "feature-") || strings.Contains(label, "--") {
			t.Errorf("%s's label %q, want the name cut short and the app kept", host.App, label)
		}
		if !isSignedBy(testPreviewKey, label) {
			t.Errorf("%q does not verify: the MAC covers the label as shortened", label)
		}
	}
	if err := site.RefuseOverlongLabels(site.ListHosts(name, "abcdefghijklmnop", []string{"web"})); err != nil {
		t.Errorf("RefuseOverlongLabels = %v, want nil: a project's own wildcard shortens the name to fit", err)
	}
}

func TestSharedPreviewRefusesASlugTooLongForOneDNSLabel(t *testing.T) {
	t.Parallel()

	slug := strings.Repeat("s", 40)
	site := NewSharedPreviewSite(slug, "preview.acme.com", testPreviewKey)
	err := site.RefuseOverlongLabels(site.ListHosts("pr-12", "abcdefghijklmnop", nil))
	if err == nil {
		t.Fatal("RefuseOverlongLabels = nil, want the refusal DNS would raise")
	}
	for _, want := range []string{"65 characters", "63", `project "` + slug + `"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("RefuseOverlongLabels = %q, want it to name %s", err, want)
		}
	}
}

func TestPreviewSiteServesNothingWithoutABaseDomainOrToken(t *testing.T) {
	t.Parallel()

	for name, hosts := range map[string][]PreviewHost{
		"no base domain": NewSharedPreviewSite("shop", "", testPreviewKey).ListHosts("pr-12", "abcdefghijklmnop", nil),
		"no token":       NewProjectPreviewSite("preview.acme.com", testPreviewKey).ListHosts("pr-12", "", nil),
	} {
		if len(hosts) != 0 {
			t.Errorf("%s: Hosts = %+v, want none", name, hosts)
		}
	}
	if NewSharedPreviewSite("shop", "", testPreviewKey).IsServing() {
		t.Error("a site with no base domain serves, want not")
	}
}

func TestPreviewKeySignsTheHMACEveryEdgeRecomputes(t *testing.T) {
	t.Parallel()

	for prefix, want := range map[string]string{
		"pr-12-web": "pr-12-web-abcdefghijklmnopp3347l26",
		"shop":      "shop-abcdefghijklmnoproheemcq",
	} {
		if got := testPreviewKey.Sign(prefix, "abcdefghijklmnop"); got != want {
			t.Errorf("Sign(%q) = %q, want %q: the first 40 bits of HMAC-SHA256 over the label in front of them, in lowercase base32", prefix, got, want)
		}
	}
}

func TestNewPreviewTokenIsSixteenBase32CharactersDrawnAfresh(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for range 64 {
		token, err := NewPreviewToken()
		if err != nil {
			t.Fatalf("NewPreviewToken: %v", err)
		}
		if len(token) != 16 || strings.Trim(token, "abcdefghijklmnopqrstuvwxyz234567") != "" {
			t.Fatalf("NewPreviewToken = %q, want 16 lowercase base32 characters: a DNS label is case-insensitive", token)
		}
		if seen[token] {
			t.Fatalf("NewPreviewToken returned %q twice", token)
		}
		seen[token] = true
	}
}

func TestNewPreviewKeyIsAFreshSecret(t *testing.T) {
	t.Parallel()

	first, err := NewPreviewKey()
	if err != nil {
		t.Fatalf("NewPreviewKey: %v", err)
	}
	second, err := NewPreviewKey()
	if err != nil {
		t.Fatalf("NewPreviewKey: %v", err)
	}
	if len(first) != 64 || first == second {
		t.Errorf("NewPreviewKey = %q then %q, want two different 256-bit keys written as hex", first, second)
	}
}

func TestPreviewKeyBindsTheMACToTheKeyAndEveryCharacterInFrontOfIt(t *testing.T) {
	t.Parallel()

	label := testPreviewKey.Sign("pr-12-web", "abcdefghijklmnop")
	for _, other := range []string{
		testPreviewKey.Sign("pr-12-api", "abcdefghijklmnop"),
		testPreviewKey.Sign("pr-13-web", "abcdefghijklmnop"),
		testPreviewKey.Sign("shop", "abcdefghijklmnop"),
		PreviewKey("another key").Sign("pr-12-web", "abcdefghijklmnop"),
	} {
		if other[len(other)-PreviewTailLen:] == label[len(label)-PreviewTailLen:] {
			t.Errorf("%q and %q end in the same MAC, want a MAC that changes with the key and every character it covers", other, label)
		}
	}
}

func isSignedBy(key PreviewKey, label string) bool {
	host := PreviewHost{Hostname: label}
	tail := host.ReadTail()
	return tail != "" && key.Sign(host.ReadPrefix(), tail[:PreviewTokenLen]) == label
}
