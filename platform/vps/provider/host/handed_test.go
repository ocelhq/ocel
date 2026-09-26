package host

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func noted(box *bench, answer string) {
	imaged := box.answer
	box.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, quoted(HandedNote(edge.ClassProduction, physical))) && strings.Contains(command, "echo "+handedUnknown) {
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
	box := runningWith(t, spec)
	note := HandedNote(spec.Class, spec.Name)
	wrote := box.at("install -D -m 0600 /dev/stdin " + quoted(note))
	handed := box.at("install -m 0600 /dev/stdin " + quoted(EnvFile(spec.Class, spec.Name)))
	ran := box.at(quoted("run") + " " + quoted("--detach"))
	if wrote < 0 || handed < 0 || ran < 0 || wrote < handed || wrote > ran {
		t.Fatalf("the note was written at %d, the values at %d and the container run at %d: the note is what a later promotion reads once the container is gone, so it is written before there is a container to lose", wrote, handed, ran)
	}
	box.mu.Lock()
	fed := box.fed[wrote]
	box.mu.Unlock()
	parsed, err := ParseNote([]byte(fed))
	if err != nil {
		t.Fatalf("the note reads %q, which the box cannot read back: %v", fed, err)
	}
	if want := []string{"API_TOKEN", "REGION"}; !slices.Equal(parsed.Handed, want) {
		t.Errorf("the note names %v as handed, want %v: the baked names alone, sorted, and nothing the container reads live", parsed.Handed, want)
	}
	if string(parsed.Live) != string(spec.Manifest) {
		t.Errorf("the note names the manifest %s, want %s: a promotion that re-creates the container hands it the same manifest", parsed.Live, spec.Manifest)
	}
	if strings.Contains(fed, sensitiveValue) {
		t.Errorf("the note contains %q, and a note that outlives the container it describes is a value left on disk", sensitiveValue)
	}
	if !strings.HasPrefix(note, StateDir(spec.Class)+"/") || strings.HasSuffix(note, envFileSuffix) {
		t.Errorf("the note is written at %s, which the sweep of env files would take with the values a deploy left", note)
	}

	if err := box.host().TakeDown(context.Background(), spec.Class, spec.Name); err != nil {
		t.Fatalf("TakeDown() = %v", err)
	}
	if box.at("rm -f "+quoted(note)) < 0 {
		t.Errorf("taking the container down left its note behind: %v", box.commands())
	}
}

func TestAPromotionOfAnAppHandedABakedValueWhoseContainerIsGoneIsRefusedByName(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	imaging(box, "false ")
	noted(box, "known\n"+`{"handed":["API_TOKEN"],"live":{"slug":"shop","class":"production","keys":[{"key":"DATABASE_URL"}]}}`+"\n")
	err := box.host().RunContainer(context.Background(), promoted())
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("a promotion of an app whose record declares no variable started it without the value its deploy baked in: %v\n%v", err, box.commands())
	}
	for _, want := range []string{"API_TOKEN", "ocel deploy", physical} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal reads %q and never names %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("the refusal reads %q and names a value the container reads live, which no promotion needs to pass", err)
	}
	if box.at(quoted("run")+" "+quoted("--detach")) >= 0 {
		t.Errorf("a refused promotion still ran a container: %v", box.commands())
	}
}

func TestAPromotionOfALiveOnlyAppWhoseContainerIsGoneRunsItAgainWithTheManifestItsDeployHanded(t *testing.T) {
	t.Parallel()

	manifest := `{"slug":"shop","class":"production","keys":[{"key":"DATABASE_URL"}],"bindings":[{"name":"main","key":"OCEL_RESOURCE_POSTGRES_main","type":"BINDING_TYPE_POSTGRES"}]}`
	box := machine(nil)
	imaging(box, "false ")
	noted(box, "known\n"+`{"handed":[],"live":`+manifest+`}`+"\n")
	spec := promoted()
	spec.HealthPath = "/healthz"
	spec.Declared = []string{"DATABASE_URL"}
	if err := box.host().RunContainer(context.Background(), spec); err != nil {
		t.Fatalf("RunContainer() of an app that reads everything live = %v, want it running again: the values reach it from the box, not from the deploy", err)
	}
	command := ranContainer(t, box)
	if !strings.Contains(command, quoted("--mount")) || !strings.Contains(command, LiveSocketDir) {
		t.Errorf("the re-created container runs %q and is handed no socket to read its values through", command)
	}
	file := wrote(t, box, EnvFile(spec.Class, spec.Name))
	if !strings.Contains(file, "OCEL_LIVE_MANIFEST="+manifest) || !strings.Contains(file, "OCEL_HEALTH_PATH=/healthz") {
		t.Errorf("the re-created container is handed %q, want the manifest its deploy noted and the health path the record names", file)
	}
	if box.at("install -D -m 0600") >= 0 {
		t.Errorf("a promotion re-wrote the note its own deploy left: %v", box.commands())
	}
}

func TestAPromotionOfAContainerThisBoxHasNoNoteForIsRefusedRatherThanGuessedEmpty(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	imaging(box, "false ")
	noted(box, "unknown\n")
	err := box.host().RunContainer(context.Background(), promoted())
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("a promotion of a container nothing on this box describes started it: %v\n%v", err, box.commands())
	}
	if !strings.Contains(err.Error(), "no note") || !strings.Contains(err.Error(), "ocel deploy") {
		t.Errorf("the refusal reads %q and does not say the note is missing or what to run", err)
	}
	if box.at(quoted("run")+" "+quoted("--detach")) >= 0 {
		t.Errorf("a refused promotion still ran a container: %v", box.commands())
	}
}

func TestAPromotionWhoseRecordDeclaresNamesIsRefusedByThemWhenTheBoxHasNoNote(t *testing.T) {
	t.Parallel()

	spec := promoted()
	spec.Declared = []string{"API_TOKEN"}
	box := machine(nil)
	imaging(box, "false ")
	noted(box, "unknown\n")
	err := box.host().RunContainer(context.Background(), spec)
	if err == nil {
		t.Fatal("a promotion of a gone container that declares a variable started it")
	}
	if !strings.Contains(err.Error(), "API_TOKEN") {
		t.Errorf("the refusal reads %q and never names what the record declares", err)
	}
	if box.at("echo "+handedUnknown) < 0 {
		t.Errorf("a promotion refused a container without reading the note, and the record cannot tell a baked name from one read live: %v", box.commands())
	}
}
