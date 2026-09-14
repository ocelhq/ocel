package host

import (
	"context"
	"errors"
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
	if want := "API_TOKEN\nDATABASE_URL\nOCEL_RESOURCE_POSTGRES_main\nREGION\n"; fed != want {
		t.Errorf("the note reads %q, want %q: the names alone, sorted, the binding's among them", fed, want)
	}
	for _, value := range []string{secretValue, sensitiveValue} {
		if strings.Contains(fed, value) {
			t.Errorf("the note carries %q, and a note that outlives the container it describes is a value left on disk", value)
		}
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

func TestAPromotionOfABindingsOnlyAppWhoseContainerIsGoneIsRefusedByTheNamesItsDeployHanded(t *testing.T) {
	t.Parallel()

	stand := machine(nil)
	imaging(stand, "false ")
	noted(stand, "held\nOCEL_RESOURCE_POSTGRES_main\n")
	err := stand.host().StandUp(context.Background(), promoted())
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeNotReady {
		t.Fatalf("a promotion of an app whose record declares no variable stood it up with none of the bindings its deploy handed it: %v\n%v", err, stand.commands())
	}
	for _, want := range []string{"OCEL_RESOURCE_POSTGRES_main", "ocel deploy", physical} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal reads %q and never names %q", err, want)
		}
	}
	if stand.at(quoted("run")+" "+quoted("--detach")) >= 0 {
		t.Errorf("a refused promotion still ran a container: %v", stand.commands())
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

func TestAPromotionWhoseRecordDeclaresNamesNeverAsksTheBox(t *testing.T) {
	t.Parallel()

	spec := promoted()
	spec.Declared = []string{"API_TOKEN"}
	stand := machine(nil)
	imaging(stand, "false ")
	if err := stand.host().StandUp(context.Background(), spec); err == nil {
		t.Fatal("a promotion of a gone container that declares a variable stood it up")
	}
	if stand.at("echo "+handedUnknown) >= 0 {
		t.Errorf("a promotion the record already refuses read the note anyway: %v", stand.commands())
	}
}
