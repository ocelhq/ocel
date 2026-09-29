package host

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

var shieldedOrigins = []proxy.OriginFile{
	{Path: coolifyDynamic + "/" + switchboard.OriginPrefix + "shop.example.com-aaa.pem", Bundle: []byte("SHOP CERTIFICATE\nSHOP KEY\n")},
	{Path: coolifyDynamic + "/" + switchboard.OriginPrefix + "_.preview.example.com-bbb.pem", Bundle: []byte("PREVIEW CERTIFICATE\nPREVIEW KEY\n")},
}

type loggingShell struct {
	*placingShell
	calls, feeds string
}

func shellLogging(t *testing.T) *loggingShell {
	t.Helper()
	box := &loggingShell{placingShell: shellPlacing(t)}
	box.calls, box.feeds = filepath.Join(box.dir, "calls"), filepath.Join(box.dir, "feeds")
	executable(t, filepath.Join(box.bin, "docker"), "#!/bin/sh\n"+
		"printf '%s\\n' \"$*\" >> "+quoted(box.calls)+"\n"+
		"if [ \"$2\" = -i ]; then cat >> "+quoted(box.feeds)+"; printf '|\\n' >> "+quoted(box.feeds)+"; fi\n")
	return box
}

func (b *loggingShell) called(t *testing.T) []string {
	t.Helper()
	return strings.Split(strings.TrimSpace(fileContents(t, b.calls)), "\n")
}

func (b *loggingShell) fedEach(t *testing.T) []string {
	t.Helper()
	return strings.Split(strings.TrimSuffix(fileContents(t, b.feeds), "|\n"), "|\n")
}

func switchboardSaid(argv ...string) string {
	return strings.Join(switchboardFed(argv...)[1:], " ")
}

func switchboardAsked(argv ...string) string {
	return strings.Join(switchboardCommand(argv...)[1:], " ")
}

func wantPlacedOrigins(t *testing.T, box *loggingShell, rendering string) {
	t.Helper()
	want := []string{
		switchboardSaid("place-origin", shieldedOrigins[0].Path),
		switchboardSaid("place-origin", shieldedOrigins[1].Path),
		switchboardAsked(append([]string{"unplace-origins"}, originPaths(shieldedOrigins)...)...),
		switchboardSaid("place", coolifyFile),
	}
	if got := box.called(t); !slices.Equal(got, want) {
		t.Errorf("docker was asked\n%q\nwant\n%q\nEach origin certificate is placed before the file naming it, and the ones no file names are unplaced", got, want)
	}
	fed := []string{string(shieldedOrigins[0].Bundle), string(shieldedOrigins[1].Bundle), rendering}
	if got := box.fedEach(t); !slices.Equal(got, fed) {
		t.Errorf("the switchboard was fed %q, want %q", got, fed)
	}
}

func TestTheWritePlacesEachOriginCertificateBeforeTheRenderingThatNamesIt(t *testing.T) {
	t.Parallel()

	box := shellLogging(t)
	before := fileContents(t, box.table)
	after := string(mustWrite(t, previewing()))
	fed := pairFed(routingPair{table: []byte(after), config: []byte("routes shop.example.com\n"), origins: shieldedOrigins})

	if code, _, errs := box.run(t, stagedWrite(tableDigest(digested(before)), coolifyFile, originPaths(shieldedOrigins)), fed); code != 0 {
		t.Fatalf("the write = %d: %s", code, errs)
	}
	wantPlacedOrigins(t, box, "routes shop.example.com\n")
}

func TestAPlacementAlonePlacesEachOriginCertificateBeforeTheRenderingThatNamesIt(t *testing.T) {
	t.Parallel()

	box := shellLogging(t)
	current := fileContents(t, box.table)

	if code, _, errs := box.run(t, replacement(tableDigest(digested(current)), coolifyFile, originPaths(shieldedOrigins)),
		originsFed(shieldedOrigins)+"routes shop.example.com\n"); code != 0 {
		t.Fatalf("the placement = %d: %s", code, errs)
	}
	wantPlacedOrigins(t, box, "routes shop.example.com\n")
}

func TestAWriteWithNoOriginCertificateUnplacesTheOnesAnEarlierRenderingNamed(t *testing.T) {
	t.Parallel()

	box := shellLogging(t)
	before := fileContents(t, box.table)
	fed := pairFed(routingPair{table: []byte(before), config: []byte("routes shop.example.com\n")})

	if code, _, errs := box.run(t, stagedWrite(tableDigest(digested(before)), coolifyFile, nil), fed); code != 0 {
		t.Fatalf("the write = %d: %s", code, errs)
	}
	want := []string{switchboardAsked("unplace-origins"), switchboardSaid("place", coolifyFile)}
	if got := box.called(t); !slices.Equal(got, want) {
		t.Errorf("docker was asked %q, want %q", got, want)
	}
}

func TestAWriteToOcelsOwnProxyConfigPlacesNoOriginCertificate(t *testing.T) {
	t.Parallel()

	if script := stagedWrite("a-digest", ProxyConfig, nil); strings.Contains(script, "origin") {
		t.Errorf("the write to %s is\n%s\nwant no origin certificate placed: ocel's own proxy holds them in its config", ProxyConfig, script)
	}
}

func TestADeployPlacesTheOriginCertificatesYourProxyNamesWithTheRenderingThatNamesThem(t *testing.T) {
	t.Parallel()

	adopted := adoptedBox(t, routed())
	h := adopted.host()
	h.front = userProxy{file: coolifyFile, origins: shieldedOrigins}
	if err := h.ClaimHosts(t.Context(), []HostClaim{{Hostname: "shop.example.com", Owner: surface, Pointer: pointed}}); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	written := slices.IndexFunc(adopted.commands(), func(command string) bool { return writesProxy(command) && places(command) })
	if written < 0 {
		t.Fatalf("the claim never placed the rendering: %q", adopted.commands())
	}
	command := adopted.commands()[written]
	for _, path := range originPaths(shieldedOrigins) {
		if !strings.Contains(command, quoted("place-origin")+" "+quoted(path)) {
			t.Errorf("the write\n%s\nplaces no %s", command, path)
		}
	}
	fed := strings.Join(adopted.feeds(), "\n")
	for _, origin := range shieldedOrigins {
		if !strings.Contains(fed, base64.StdEncoding.EncodeToString(origin.Bundle)) {
			t.Errorf("the deploy fed %q, want %s's bundle in it", fed, origin.Path)
		}
	}
}

func TestDestroyUnplacesTheOriginCertificatesBesideOcelCaddyOnceYourCaddyNoLongerNamesThem(t *testing.T) {
	t.Parallel()

	dir, bin := t.TempDir(), t.TempDir()
	executable(t, filepath.Join(bin, "docker"), "#!/bin/sh\n[ \"$1\" = inspect ] && echo true\n[ \"$2\" = "+SwitchboardContainer+" ] && exit 1\nexit 0\n")
	for _, name := range []string{switchboard.OriginPrefix + "shop.example.com-aaa.pem", "yours.pem"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	filled := CaddyFront{Directory: dir, Container: "caddy", Network: "web"}.Filled()
	taken := Front{Caddy: &filled}.placedRemovals()[0]
	if command := taken.command(); !strings.Contains(command, quoted("unplace-origins")) {
		t.Errorf("the destroy runs\n%s\nwant the origin certificates unplaced through the switchboard", command)
	}
	run := exec.Command("sh", "-c", taken.command())
	run.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	if said, err := run.CombinedOutput(); err != nil {
		t.Fatalf("the removal failed: %v\n%s", err, said)
	}
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].Name() != "yours.pem" {
		t.Errorf("the directory holds %v after the destroy, want yours.pem alone: an origin certificate's key never outlives ocel.caddy", left)
	}
}
