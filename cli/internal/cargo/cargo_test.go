package cargo

import (
	"strings"
	"testing"
)

func crateWithBins(names ...string) Package {
	crate := Package{Name: "server", Targets: []Target{{Name: "server", Kind: []string{"lib"}}}}
	for _, name := range names {
		crate.Targets = append(crate.Targets, Target{Name: name, Kind: []string{"bin"}})
	}
	return crate
}

func TestFindBinTakesTheOneBinaryACrateBuildsWhenNoneIsNamed(t *testing.T) {
	bin, err := crateWithBins("api").FindBin("")
	if err != nil || bin.Name != "api" {
		t.Fatalf("FindBin(\"\") = %+v, %v, want the crate's one binary api", bin, err)
	}
}

func TestFindBinTakesTheNamedBinaryAmongSeveral(t *testing.T) {
	bin, err := crateWithBins("api", "worker").FindBin("worker")
	if err != nil || bin.Name != "worker" {
		t.Fatalf("FindBin(worker) = %+v, %v, want the binary named worker", bin, err)
	}
}

func TestFindBinRefusesAnUnnamedChoiceAmongSeveralBinariesAndAsksForAnEntrypoint(t *testing.T) {
	_, err := crateWithBins("api", "worker").FindBin("")
	if err == nil {
		t.Fatal("FindBin(\"\") chose one of two binaries")
	}
	for _, want := range []string{"server", `"api"`, `"worker"`, `"entrypoint"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("FindBin(\"\") = %v, want it to name %s", err, want)
		}
	}
}

func TestFindBinRefusesANameNoBinaryHasAndListsTheBinaries(t *testing.T) {
	_, err := crateWithBins("api", "worker").FindBin("web")
	if err == nil {
		t.Fatal("FindBin(web) found a binary the crate does not build")
	}
	for _, want := range []string{"server", `"web"`, `"api"`, `"worker"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("FindBin(web) = %v, want it to name %s", err, want)
		}
	}
}

func TestFindBinRefusesACrateThatBuildsNoBinary(t *testing.T) {
	for _, named := range []string{"", "api"} {
		_, err := crateWithBins().FindBin(named)
		if err == nil || !strings.Contains(err.Error(), "server") || !strings.Contains(err.Error(), "no binary") {
			t.Errorf("FindBin(%q) = %v, want a refusal naming the crate server and that it builds no binary", named, err)
		}
	}
}

func TestFindBinTakesTheBinaryTheCrateRunsByDefaultWhenNoneIsNamed(t *testing.T) {
	crate := crateWithBins("api", "worker")
	crate.DefaultRun = "worker"

	bin, err := crate.FindBin("")
	if err != nil || bin.Name != "worker" {
		t.Fatalf("FindBin(\"\") = %+v, %v, want worker, the binary default-run names — cargo run and railpack start it", bin, err)
	}
}

func TestFindBinTakesTheNamedBinaryOverTheOneTheCrateRunsByDefault(t *testing.T) {
	crate := crateWithBins("api", "worker")
	crate.DefaultRun = "worker"

	bin, err := crate.FindBin("api")
	if err != nil || bin.Name != "api" {
		t.Fatalf("FindBin(api) = %+v, %v, want api, the binary the entrypoint names", bin, err)
	}
}
