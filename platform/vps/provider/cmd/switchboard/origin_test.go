package main

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const bundle = "-----BEGIN CERTIFICATE-----\nORIGIN\n-----END CERTIFICATE-----\n-----BEGIN PRIVATE KEY-----\nKEY\n-----END PRIVATE KEY-----\n"

func TestAnOriginCertificateIsPlacedReadableByTheDirectorysGroupAndNoOneElse(t *testing.T) {
	dir := placing(t)
	at := filepath.Join(dir, switchboard.OriginPrefix+"shop.example.com-0123456789ab.pem")

	if code, _, errs := fed(t, strings.NewReader(bundle), "place-origin", at); code != 0 {
		t.Fatalf("place-origin = %d: %s", code, errs)
	}
	if got := contents(t, at); got != bundle {
		t.Errorf("%s reads %q, want the bundle it was fed", at, got)
	}
	placed, err := os.Stat(at)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := placed.Mode().Perm(); mode != 0o640 {
		t.Errorf("%s is mode %v, want 0640: it holds the origin certificate's key", at, mode)
	}
	if got, want := placed.Sys().(*syscall.Stat_t).Gid, directory.Sys().(*syscall.Stat_t).Gid; got != want {
		t.Errorf("%s is group %d, want %d, the group of the directory your proxy reads", at, got, want)
	}
	if got := entries(t, dir); !slices.Equal(got, []string{filepath.Base(at)}) {
		t.Errorf("the directory contains %q, want the bundle alone", got)
	}
}

func TestAnOriginCertificateIsPlacedOnlyUnderAnOriginName(t *testing.T) {
	dir := placing(t)
	for _, name := range []string{"ocel.caddy", switchboard.OriginPrefix + "shop.example.com.caddy", "shop.example.com.pem"} {
		if code, _, errs := fed(t, strings.NewReader(bundle), "place-origin", filepath.Join(dir, name)); code != exitRefused || !strings.Contains(errs, switchboard.OriginPrefix) {
			t.Errorf("place-origin %s = %d, %q, want refused naming %s*.pem, the only names a key is placed under", name, code, errs, switchboard.OriginPrefix)
		}
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Errorf("the directory contains %q after the refusals, want nothing placed", got)
	}
}

func TestUnplacingOriginsRemovesEveryOriginCertificateButTheOnesKeptAndNothingElse(t *testing.T) {
	dir := placing(t)
	kept := filepath.Join(dir, switchboard.OriginPrefix+"shop.example.com-new.pem")
	for _, name := range []string{"ocel.caddy", "yours.pem", filepath.Base(kept), switchboard.OriginPrefix + "shop.example.com-old.pem", switchboard.OriginPrefix + "gone.example.com-1.pem"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	if code, _, errs := ran(t, "unplace-origins", kept); code != 0 {
		t.Fatalf("unplace-origins = %d: %s", code, errs)
	}
	if got, want := entries(t, dir), []string{filepath.Base(kept), "ocel.caddy", "yours.pem"}; !slices.Equal(got, want) {
		t.Errorf("the directory contains %q, want %q: only origin certificates nothing keeps are unplaced", got, want)
	}

	if code, _, errs := ran(t, "unplace-origins"); code != 0 {
		t.Fatalf("unplace-origins of everything = %d: %s", code, errs)
	}
	if got, want := entries(t, dir), []string{"ocel.caddy", "yours.pem"}; !slices.Equal(got, want) {
		t.Errorf("the directory contains %q, want %q", got, want)
	}
}

func TestTheOriginGroupIsTheGroupOfTheDirectoryOriginCertificatesArePlacedIn(t *testing.T) {
	dir := placing(t)
	directory, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	code, out, errs := ran(t, "origin-group")
	if code != 0 {
		t.Fatalf("origin-group = %d: %s", code, errs)
	}
	if got, want := strings.TrimSpace(out), strconv.FormatUint(uint64(directory.Sys().(*syscall.Stat_t).Gid), 10); got != want {
		t.Errorf("origin-group = %q, want %q: place-origin runs as that group, since the switchboard holds no CAP_CHOWN to give a file a group it is not in", got, want)
	}
}

func TestUnplacingOriginsLeavesWhatIsNamedLikeOneButIsNoOriginCertificate(t *testing.T) {
	dir := placing(t)
	notes := switchboard.OriginPrefix + "notes.txt"
	if err := os.WriteFile(filepath.Join(dir, notes), []byte("yours"), 0o640); err != nil {
		t.Fatal(err)
	}
	folder := switchboard.OriginPrefix + "archive.pem"
	if err := os.MkdirAll(filepath.Join(dir, folder, "inside"), 0o750); err != nil {
		t.Fatal(err)
	}

	if code, _, errs := ran(t, "unplace-origins"); code != 0 {
		t.Fatalf("unplace-origins = %d: %s", code, errs)
	}
	if got, want := entries(t, dir), []string{folder, notes}; !slices.Equal(got, want) {
		t.Errorf("the directory contains %q, want %q: only a file named %s*.pem is an origin certificate", got, want, switchboard.OriginPrefix)
	}
}
