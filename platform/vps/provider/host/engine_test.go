package host

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func engineOrSkip(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath(dockerEngine); err != nil {
		t.Skip("this machine carries no docker, and what an engine reports cannot be read off one that is not here")
	}
	if err := exec.Command(dockerEngine, "info").Run(); err != nil {
		t.Skip("the docker on this machine answers nothing, so there is no engine to measure against")
	}
	for _, held := range [][]string{
		{"container", "inspect", ProxyContainer},
		{"network", "inspect", ProxyNetwork},
		{"volume", "inspect", ProxyVolume},
	} {
		if exec.Command(dockerEngine, append([]string{held[0]}, held[1:]...)...).Run() == nil {
			t.Skipf("this machine already carries the %s ocel writes, and the test will not take something it did not create", held[0])
		}
	}
}

func seenByTheEngine(t *testing.T) string {
	t.Helper()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("this machine has no home directory to hand the engine a bind source from")
	}
	dir, err := os.MkdirTemp(home, "ocel-probe-")
	if err != nil {
		t.Skip("nothing under this home directory can be written, so the engine can be handed no bind source")
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	at := filepath.Join(dir, "seen")
	if err := os.WriteFile(at, []byte("what the engine must be able to read\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	said, err := exec.Command(dockerEngine, "run", "--rm", "--volume", at+":/seen:ro",
		ProxyImage, "test", "-f", "/seen").CombinedOutput()
	if err != nil {
		t.Skipf("this machine's engine cannot read %s, so a bind source cannot be handed to it here: %s", dir, said)
	}
	return dir
}

func TestTheProbeReadsARealEngineExactlyAsTheItemStatesIt(t *testing.T) {
	engineOrSkip(t)
	dir := seenByTheEngine(t)

	config, helper := filepath.Join(dir, "caddy.json"), filepath.Join(dir, proxyHelperName)
	if err := os.WriteFile(config, proxyBaseline, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, proxyHelper(ArchAMD64), 0o750); err != nil {
		t.Fatal(err)
	}
	here := func(written string) string {
		return strings.ReplaceAll(strings.ReplaceAll(written, ProxyConfig, config), ProxyHelper, helper)
	}

	if out, err := exec.Command(dockerEngine, "network", "create", ProxyNetwork).CombinedOutput(); err != nil {
		t.Fatalf("create the network every deploy resolves across: %v\n%s", err, out)
	}
	t.Cleanup(func() { exec.Command(dockerEngine, "network", "rm", ProxyNetwork).Run() })
	if out, err := exec.Command(dockerEngine, "volume", "create", ProxyVolume).CombinedOutput(); err != nil {
		t.Fatalf("create the volume the proxy persists into: %v\n%s", err, out)
	}
	t.Cleanup(func() { exec.Command(dockerEngine, "volume", "rm", ProxyVolume).Run() })
	t.Cleanup(func() { exec.Command(dockerEngine, "rm", "--force", ProxyContainer).Run() })

	if out, err := exec.Command("/bin/sh", "-c", here(containerCommand())).CombinedOutput(); err != nil {
		t.Fatalf("the write that stands the proxy up = %v\n%s", err, out)
	}

	rendered, err := exec.Command("/bin/sh", "-c", containerProbe()).Output()
	if err != nil {
		t.Fatalf("probe the proxy this machine is running: %v", err)
	}
	observed, _, err := readSurvey(string(rendered))
	if err != nil {
		t.Fatal(err)
	}

	stated := containerItem()
	stated.Content = proxyFactsOver([]string{
		config + ":" + proxyConfigMount + ":ro",
		helper + ":" + ProxyHelperMount + ":ro",
		ProxyVolume + ":" + proxyDataMount,
	})
	if observed[stated.ID()] != stated.Digest() {
		box, _ := exec.Command(dockerEngine, "inspect", "--type", "container", "--format", ProxyFactTemplate, ProxyContainer).Output()
		t.Errorf("a real engine reports the proxy as something other than the item ocel writes it from, so every re-run plans an update over a proxy that stands:\n%s",
			compared(canonical(string(box)), strings.TrimSpace(string(stated.Content))))
	}
}

func canonical(rendered string) string {
	lines := strings.Split(strings.TrimSpace(rendered), "\n")
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

func compared(box, stated string) string {
	said, want := strings.Split(box, "\n"), strings.Split(stated, "\n")
	var written strings.Builder
	for at := 0; at < max(len(said), len(want)); at++ {
		var read, meant string
		if at < len(said) {
			read = said[at]
		}
		if at < len(want) {
			meant = want[at]
		}
		mark := "  "
		if read != meant {
			mark = "! "
		}
		written.WriteString(mark + "box:  " + read + "\n" + mark + "item: " + meant + "\n")
	}
	return written.String()
}
