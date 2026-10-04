//go:build integration

package vps_test

import (
	"archive/tar"
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const (
	fixtureBase = "ocel-live-app:base-ocel"
	fixtureRepo = "ocel-live-app"
)

func (vm machine) feeds(t *testing.T, command string, stdin []byte) string {
	t.Helper()
	run := exec.Command("ssh",
		"-F", vm.config, "-i", vm.key,
		"-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes",
		vm.user+"@"+vm.addr, command)
	run.Stdin = bytes.NewReader(stdin)
	var errs strings.Builder
	run.Stderr = &errs
	said, err := run.Output()
	if err != nil {
		t.Fatalf("ssh %q: %v\n%s", command, err, errs.String())
	}
	return string(said)
}

func (vm machine) arch(t *testing.T) string {
	t.Helper()
	switch reported := strings.TrimSpace(vm.ssh(t, "uname -m")); reported {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	default:
		t.Skipf("no fixture image is built for a box reporting %q", reported)
		return ""
	}
}

func fixtureBinary(t *testing.T, arch string) []byte {
	t.Helper()
	read, err := os.ReadFile(fixtureBuilt(t, "linux", arch))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := host.ContainerRuntime(arch)
	if err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	written := tar.NewWriter(&raw)
	for name, body := range map[string][]byte{"app": read, strings.TrimPrefix(containerimage.RuntimePath, "/"): runtime} {
		if err := written.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := written.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := written.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func fixtures(t *testing.T, vm machine) {
	t.Helper()
	present := map[string]bool{}
	for _, tagged := range lines(vm.ssh(t, "sudo docker image ls --format '{{.Repository}}:{{.Tag}}' "+fixtureRepo)) {
		present[strings.TrimSpace(tagged)] = true
	}
	if !present[fixtureBase] {
		vm.feeds(t, "sudo docker import --change 'ENTRYPOINT [\""+containerimage.RuntimePath+"\", \"/app\"]' - "+fixtureBase+" >/dev/null",
			fixtureBinary(t, vm.arch(t)))
	}
	for tag, envs := range map[string][]string{
		"one":     {"RELEASE=one"},
		"two":     {"RELEASE=two"},
		"sick":    {"RELEASE=sick", "HEALTH_STATUS=404"},
		"hung":    {"MODE=hang"},
		"crasher": {"MODE=crash"},
	} {
		if present[fixtureAt(tag)] {
			continue
		}
		file := "FROM " + fixtureBase + "\n"
		for _, env := range envs {
			file += "ENV " + env + "\n"
		}
		vm.feeds(t, "sudo docker build -q -t "+fixtureAt(tag)+" - >/dev/null", []byte(file))
	}
}

func fixtureAt(tag string) string { return fixtureRepo + ":" + tag }
