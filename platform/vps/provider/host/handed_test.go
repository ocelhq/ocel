package host

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func noted(stand *bench, answer string) {
	imaged := stand.answer
	stand.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, quoted(HandedNote(providerkit.ClassProduction, physical))) && strings.Contains(command, "echo "+handedUnknown) {
			return session.Result{Stdout: answer}, true
		}
		if imaged != nil {
			return imaged(command)
		}
		return session.Result{}, false
	}
}

func TestADeployNotesTheNamesItHandedBeforeTheRunAndATakeDownForgetsThem(t *testing.T) {
	t.Parallel()

	spec := valued()
	stand := standingWith(t, spec)
	note := HandedNote(spec.Class, spec.Name)
	wrote := stand.at("install -D -m 0600 /dev/stdin " + quoted(note))
	handed := stand.at("install -m 0600 /dev/stdin " + quoted(EnvFile(spec.Class, spec.Name)))
	ran := stand.at(quoted("run") + " " + quoted("--detach"))
	if wrote < 0 || handed < 0 || ran < 0 || wrote < handed || wrote > ran {
		t.Fatalf("the note was written at %d, the values at %d and the container run at %d: the note is what a later promotion reads once the container is gone, so it is written before there is a container to lose", wrote, handed, ran)
	}
	stand.mu.Lock()
	fed := stand.fed[wrote]
	stand.mu.Unlock()
	parsed, err := ParseNote([]byte(fed))
	if err != nil {
		t.Fatalf("the note reads %q, which the box cannot read back: %v", fed, err)
	}
	if want := []string{"API_TOKEN", "REGION"}; !slices.Equal(parsed.Handed, want) {
		t.Errorf("the note names %v as handed, want %v: the baked names alone, sorted, and nothing the container reads live", parsed.Handed, want)
	}
	if string(parsed.Live) != string(spec.Manifest) {
		t.Errorf("the note carries the manifest %s, want %s: a promotion that re-creates the container hands it the same manifest", parsed.Live, spec.Manifest)
	}
	if strings.Contains(fed, sensitiveValue) {
		t.Errorf("the note carries %q, and a note that outlives the container it describes is a value left on disk", sensitiveValue)
	}
	if !strings.HasPrefix(note, StateDir(spec.Class)+"/") || strings.HasSuffix(note, envFileSuffix) {
		t.Errorf("the note stands at %s, which the sweep of env files would take with the values a deploy left", note)
	}

	if err := stand.host().TakeDown(context.Background(), spec.Class, spec.Name); err != nil {
		t.Fatalf("TakeDown() = %v", err)
	}
	if stand.at("rm -f "+quoted(note)) < 0 {
		t.Errorf("taking the container down left its note behind: %v", stand.commands())
	}
}

func TestAPromotionOfAnAppHandedABakedValueWhoseContainerIsGoneIsRefusedByName(t *testing.T) {
	t.Parallel()

	stand := machine(nil)
	imaging(stand, "false ")
	noted(stand, "held\n"+`{"handed":["API_TOKEN"],"live":{"slug":"shop","class":"production","keys":[{"key":"DATABASE_URL"}]}}`+"\n")
	err := stand.host().StandUp(context.Background(), promoted())
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeNotReady {
		t.Fatalf("a promotion of an app whose record declares no variable stood it up without the value its deploy baked in: %v\n%v", err, stand.commands())
	}
	for _, want := range []string{"API_TOKEN", "ocel deploy", physical} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal reads %q and never names %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("the refusal reads %q and names a value the container reads live, which no promotion needs to carry", err)
	}
	if stand.at(quoted("run")+" "+quoted("--detach")) >= 0 {
		t.Errorf("a refused promotion still ran a container: %v", stand.commands())
	}
}

func TestAPromotionOfALiveOnlyAppWhoseContainerIsGoneStandsItBackUpWithTheManifestItsDeployHanded(t *testing.T) {
	t.Parallel()

	manifest := `{"slug":"shop","class":"production","keys":[{"key":"DATABASE_URL"}],"bindings":[{"name":"main","key":"OCEL_RESOURCE_POSTGRES_main","type":"BINDING_TYPE_POSTGRES"}]}`
	stand := machine(nil)
	imaging(stand, "false ")
	noted(stand, "held\n"+`{"handed":[],"live":`+manifest+`}`+"\n")
	spec := promoted()
	spec.HealthPath = "/healthz"
	spec.Declared = []string{"DATABASE_URL"}
	if err := stand.host().StandUp(context.Background(), spec); err != nil {
		t.Fatalf("StandUp() of an app that reads everything live = %v, want it stood back up: the values reach it from the box, not from the deploy", err)
	}
	command := ranContainer(t, stand)
	if !strings.Contains(command, quoted("--mount")) || !strings.Contains(command, LiveSocketDir) {
		t.Errorf("the re-created container runs %q and is handed no socket to read its values through", command)
	}
	file := wrote(t, stand, EnvFile(spec.Class, spec.Name))
	if !strings.Contains(file, "OCEL_LIVE_MANIFEST="+manifest) || !strings.Contains(file, "OCEL_HEALTH_PATH=/healthz") {
		t.Errorf("the re-created container is handed %q, want the manifest its deploy noted and the health path the record names", file)
	}
	if stand.at("install -D -m 0600") >= 0 {
		t.Errorf("a promotion re-wrote the note its own deploy left: %v", stand.commands())
	}
}

func TestAPromotionOfAContainerThisBoxHoldsNoNoteForIsRefusedRatherThanGuessedEmpty(t *testing.T) {
	t.Parallel()

	stand := machine(nil)
	imaging(stand, "false ")
	noted(stand, "unknown\n")
	err := stand.host().StandUp(context.Background(), promoted())
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeNotReady {
		t.Fatalf("a promotion of a container nothing on this box describes stood it up: %v\n%v", err, stand.commands())
	}
	if !strings.Contains(err.Error(), "no note") || !strings.Contains(err.Error(), "ocel deploy") {
		t.Errorf("the refusal reads %q and does not say the note is missing or what to run", err)
	}
	if stand.at(quoted("run")+" "+quoted("--detach")) >= 0 {
		t.Errorf("a refused promotion still ran a container: %v", stand.commands())
	}
}

func TestAPromotionWhoseRecordDeclaresNamesIsRefusedByThemWhenTheBoxHoldsNoNote(t *testing.T) {
	t.Parallel()

	spec := promoted()
	spec.Declared = []string{"API_TOKEN"}
	stand := machine(nil)
	imaging(stand, "false ")
	noted(stand, "unknown\n")
	err := stand.host().StandUp(context.Background(), spec)
	if err == nil {
		t.Fatal("a promotion of a gone container that declares a variable stood it up")
	}
	if !strings.Contains(err.Error(), "API_TOKEN") {
		t.Errorf("the refusal reads %q and never names what the record declares", err)
	}
	if stand.at("echo "+handedUnknown) < 0 {
		t.Errorf("a promotion refused a container without reading the note, and the record cannot tell a baked name from one read live: %v", stand.commands())
	}
}
