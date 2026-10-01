package host

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func findsHealthPath(command string) bool {
	return strings.Contains(command, quoted("find-health-path"))
}

func searchedOn(found session.Result) *bench {
	box := machine(nil)
	box.answer = func(command string) (session.Result, bool) {
		if findsHealthPath(command) {
			return found, true
		}
		return session.Result{}, false
	}
	return box
}

func answeredLines(answers ...string) string {
	var lines strings.Builder
	for _, answer := range answers {
		lines.WriteString(switchboard.Answered + " " + answer + "\n")
	}
	return lines.String()
}

func aHealthPathSearch() HealthPathSearch {
	return HealthPathSearch{App: "node", Target: nextTarget, Window: 60 * time.Second}
}

func TestTheBoxIsAskedForTheHealthPathCandidatesInOrderAtTheAppsTarget(t *testing.T) {
	t.Parallel()

	box := searchedOn(session.Result{Stdout: answeredLines("/up 404", "/health 200")})
	if _, err := box.host().FindHealthPath(context.Background(), aHealthPathSearch(), nil); err != nil {
		t.Fatalf("FindHealthPath() = %v", err)
	}
	asked := box.at(quoted("find-health-path"))
	if asked < 0 {
		t.Fatalf("the box was never asked to find a health path: %v", box.commands())
	}
	want := words(switchboardCommand("find-health-path", "--deploy-timeout", "60", nextTarget, "/up", "/health", "/healthz", "/"))
	if command := box.commands()[asked]; !strings.Contains(command, want) {
		t.Errorf("the box was asked\n%s\nwant it to contain\n%s", command, want)
	}
}

func TestTheFirstPathThatIsNotAbsentIsTheHealthPathAndTheDeploySaysHowItWasFound(t *testing.T) {
	t.Parallel()

	var said []string
	box := searchedOn(session.Result{Stdout: answeredLines("/up 404", "/health 503")})
	path, err := box.host().FindHealthPath(context.Background(), aHealthPathSearch(), saying(&said))
	if err != nil {
		t.Fatalf("FindHealthPath() = %v", err)
	}
	if path != "/health" {
		t.Errorf("FindHealthPath() = %q, want /health: a 503 means the path exists and the app is not healthy yet", path)
	}
	if want := "node answers its health check on /health (found by probing /up, /health)"; !slices.Contains(said, want) {
		t.Errorf("the deploy said %q, want it to say %q", said, want)
	}
	if len(said) == 0 || !strings.HasPrefix(said[0], "Waiting up to 1m0s for node to answer HTTP") {
		t.Errorf("the deploy said %q, want it first to say what it waits for", said)
	}
}

func TestAnAppAnsweringEveryCandidateAsAbsentIsRefusedWithWhatEachAnsweredAndHowToNameOne(t *testing.T) {
	t.Parallel()

	box := searchedOn(session.Result{Code: 6, Stdout: answeredLines("/up 404", "/health 404", "/healthz 405", "/ 404")})
	_, err := box.host().FindHealthPath(context.Background(), aHealthPathSearch(), nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("FindHealthPath() = %v, want an invalid refusal", err)
	}
	for _, want := range []string{"/up answered 404", "/health answered 404", "/healthz answered 405", "/ answered 404", `"health.path"`, `"health": { "path": "/health" }`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal reads\n%s\nand never says %q", err, want)
		}
	}
}

func TestAnAppThatNeverAnswersIsRefusedWithItsStateAndLogs(t *testing.T) {
	t.Parallel()

	box := searchedOn(session.Result{Code: 4, Stderr: nextTarget + " never answered /up within 1m0s"})
	_, err := box.host().FindHealthPath(context.Background(), aHealthPathSearch(), nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("FindHealthPath() = %v, want a not-ready refusal", err)
	}
	if !strings.Contains(err.Error(), "never answered /up within 1m0s") {
		t.Errorf("the refusal reads\n%s\nand never says what the box saw", err)
	}
	if box.at(logCommand(physical)) < 0 {
		t.Errorf("the refusal read no logs off %s: %v", physical, box.commands())
	}
}

func searchedOnIdling(found session.Result, idle bool) *bench {
	box := searchedOn(found)
	searched := box.answer
	box.answer = func(command string) (session.Result, bool) {
		if idles(command) {
			if idle {
				return everyIdle(command), true
			}
			return session.Result{}, true
		}
		return searched(command)
	}
	return box
}

func removes(name string) string { return "docker rm --force " + quoted(name) }

func TestASearchThatFindsNoHealthPathRemovesTheContainerItProbedWhenNothingRoutesToIt(t *testing.T) {
	t.Parallel()

	box := searchedOnIdling(session.Result{Stdout: answeredLines("/up 404", "/health 404", "/healthz 404", "/ 405"), Code: 6}, true)
	_, err := box.host().FindHealthPath(context.Background(), aHealthPathSearch(), nil)
	if err == nil {
		t.Fatal("FindHealthPath() = nil, want every path absent refused")
	}
	name := containerOf(nextTarget)
	if box.at(removes(name)) < 0 {
		t.Errorf("the box ran %v, want %s removed: nothing promotes a release whose health path was never found", box.commands(), name)
	}
	if !strings.Contains(err.Error(), name+" removed") {
		t.Errorf("FindHealthPath() = %v, want it to say %s was removed", err, name)
	}
}

func TestASearchThatNeverHearsTheAppRemovesTheContainerItProbedWhenNothingRoutesToIt(t *testing.T) {
	t.Parallel()

	box := searchedOnIdling(session.Result{Stderr: "never answered", Code: 4}, true)
	if _, err := box.host().FindHealthPath(context.Background(), aHealthPathSearch(), nil); err == nil {
		t.Fatal("FindHealthPath() = nil, want an app that never answered refused")
	}
	if box.at(removes(containerOf(nextTarget))) < 0 {
		t.Errorf("the box ran %v, want the probed container removed", box.commands())
	}
}

func TestASearchThatFindsNoHealthPathLeavesAContainerTheProxyStillUses(t *testing.T) {
	t.Parallel()

	box := searchedOnIdling(session.Result{Stdout: answeredLines("/up 404", "/health 404", "/healthz 404", "/ 404"), Code: 6}, false)
	if _, err := box.host().FindHealthPath(context.Background(), aHealthPathSearch(), nil); err == nil {
		t.Fatal("FindHealthPath() = nil, want every path absent refused")
	}
	if box.at(removes(containerOf(nextTarget))) >= 0 {
		t.Errorf("the box ran %v, want nothing removed: the proxy still sends traffic to it", box.commands())
	}
}
