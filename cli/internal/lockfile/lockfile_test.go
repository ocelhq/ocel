package lockfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/providers"
)

const releaseChecksums = `d0d0 ocel_0.2.0_linux_amd64.tar.gz
0002 ocel-provider-aws_0.2.0_darwin_arm64.tar.gz
0001 ocel-provider-aws_0.2.0_darwin_amd64.tar.gz
0003 ocel-provider-aws_0.2.0_linux_amd64.tar.gz
0004 ocel-provider-aws_0.2.0_linux_arm64.tar.gz
0005 ocel-provider-aws_0.2.0_windows_amd64.zip
0006 ocel-provider-vps_0.2.0_linux_amd64.tar.gz
beef ocel-provider-aws_0.1.0_linux_amd64.tar.gz
`

func TestChecksumsParseTheDigestOfEveryProviderArchive(t *testing.T) {
	t.Parallel()

	sums, err := providers.ParseChecksums(strings.NewReader(releaseChecksums))
	if err != nil {
		t.Fatalf("ParseChecksums: %v", err)
	}
	if got := sums["ocel-provider-aws_0.2.0_linux_amd64.tar.gz"]; got != "0003" {
		t.Fatalf("digest = %q, want %q", got, "0003")
	}
	if _, held := sums["ocel_0.2.0_linux_amd64.tar.gz"]; !held {
		t.Fatal("the CLI's own archive was dropped; the parse reads the file, it does not filter it")
	}
}

func TestTheLockHoldsEveryProviderOfTheVersionItPins(t *testing.T) {
	t.Parallel()

	sums, err := providers.ParseChecksums(strings.NewReader(releaseChecksums))
	if err != nil {
		t.Fatalf("ParseChecksums: %v", err)
	}
	lock := FromChecksums("0.2.0", sums)

	if lock.CLI != "0.2.0" {
		t.Fatalf("lock.CLI = %q, want %q", lock.CLI, "0.2.0")
	}
	if got, held := lock.Digest("aws", "windows-amd64"); !held || got != "0005" {
		t.Fatalf("Digest(aws, windows-amd64) = %q, %v, want %q, true", got, held, "0005")
	}
	if len(lock.Providers["aws"]) != 5 {
		t.Fatalf("aws pins %d platforms, want the five the release ships", len(lock.Providers["aws"]))
	}
	if _, held := lock.Digest("aws", "linux-386"); held {
		t.Fatal("the lock pinned a platform the release does not ship")
	}
	if _, held := lock.Providers["ocel"]; held {
		t.Fatal("the CLI's own archive was pinned as a provider")
	}
}

func TestAnotherVersionIsNotPinned(t *testing.T) {
	t.Parallel()

	sums, err := providers.ParseChecksums(strings.NewReader(releaseChecksums))
	if err != nil {
		t.Fatalf("ParseChecksums: %v", err)
	}
	lock := FromChecksums("0.2.0", sums)
	for _, digest := range lock.Providers["aws"] {
		if digest == "beef" {
			t.Fatal("an archive of another version was pinned")
		}
	}
}

func TestTheLockReadsBackTheBytesItWrote(t *testing.T) {
	t.Parallel()

	sums, err := providers.ParseChecksums(strings.NewReader(releaseChecksums))
	if err != nil {
		t.Fatalf("ParseChecksums: %v", err)
	}
	dir := t.TempDir()
	written := FromChecksums("0.2.0", sums)
	if err := Write(dir, written); err != nil {
		t.Fatalf("Write: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, Name))
	if err != nil {
		t.Fatalf("read %s: %v", Name, err)
	}
	if raw[len(raw)-1] != '\n' {
		t.Error("the lock does not end in a newline")
	}

	read, held, err := Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !held {
		t.Fatal("Read found no lock where Write had just put one")
	}
	if string(read.Bytes()) != string(written.Bytes()) {
		t.Fatalf("the lock read back differs:\n%s\nwant\n%s", read.Bytes(), written.Bytes())
	}
}

func TestNoLockReadsAsNoLock(t *testing.T) {
	t.Parallel()

	_, held, err := Read(t.TempDir())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if held {
		t.Fatal("Read claimed a lock in a directory holding none")
	}
}

func TestTheLockIsWrittenInOneOrderWhateverOrderItWasBuiltIn(t *testing.T) {
	t.Parallel()

	forward, err := providers.ParseChecksums(strings.NewReader(releaseChecksums))
	if err != nil {
		t.Fatalf("ParseChecksums: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(releaseChecksums), "\n")
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	backward, err := providers.ParseChecksums(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	if err != nil {
		t.Fatalf("ParseChecksums: %v", err)
	}

	if a, b := FromChecksums("0.2.0", forward).Bytes(), FromChecksums("0.2.0", backward).Bytes(); string(a) != string(b) {
		t.Fatalf("the lock is not byte-identical across build order:\n%s\nvs\n%s", a, b)
	}
}
