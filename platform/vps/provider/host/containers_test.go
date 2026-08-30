package host

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const (
	appImage = "ocel/web:sha256-abc"
	physical = "shop-web-abc123def456"
)

func aContainer() Container {
	return Container{Name: physical, App: "web", Image: appImage}
}

func imaging(b *bench, held string) {
	b.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker inspect") && strings.Contains(command, "State.Running") {
			return session.Result{Stdout: held + "\n"}, true
		}
		return session.Result{}, false
	}
}

func stood(t *testing.T, held string) *bench {
	t.Helper()
	stand := machine(nil)
	imaging(stand, held)
	if err := stand.host().StandUp(context.Background(), aContainer()); err != nil {
		t.Fatalf("StandUp() = %v", err)
	}
	return stand
}

func ranContainer(t *testing.T, stand *bench) string {
	t.Helper()
	for _, command := range stand.commands() {
		if strings.Contains(command, quoted("run")+" "+quoted("--detach")) {
			return command
		}
	}
	t.Fatalf("nothing stood a container up: %v", stand.commands())
	return ""
}

func TestAReleaseStandsUpOneLabelledContainerOnTheOneNetworkTargetsResolveAcross(t *testing.T) {
	t.Parallel()

	command := ranContainer(t, stood(t, "false "))
	for what, wanted := range map[string]string{
		"the name the drain attributes its in-flight count to": quoted("--name") + " " + quoted(physical),
		"a reboot that does not take the app down":             quoted("--restart") + " " + quoted(appRestart),
		"the network the proxy reaches it over":                quoted("--network") + " " + quoted(ProxyNetwork),
		"the app label retention reads":                        quoted("--label") + " " + quoted(LabelApp+"=web"),
		"the ref label retention reads":                        quoted("--label") + " " + quoted(LabelRef+"="+appImage),
		"the port the app is told to bind":                     quoted("--env") + " " + quoted("PORT="+AppPort),
		"the image the release names":                          quoted(appImage),
	} {
		if !strings.Contains(command, wanted) {
			t.Errorf("standing a container up runs %q, which carries no %s (%s)", command, what, wanted)
		}
	}
}

func TestNoPortIsPublishedForAnAppContainerAndNoListenerIsAddedAnywhere(t *testing.T) {
	t.Parallel()

	command := ranContainer(t, stood(t, "false "))
	for _, opening := range []string{quoted("--publish"), quoted("-p"), "--network host", quoted("--expose")} {
		if strings.Contains(command, opening) {
			t.Errorf("standing a container up runs %q, and %q puts the app on an address the proxy is not the only way to: every probe and every request reaches it over the shared network alone",
				command, opening)
		}
	}
}

func TestAContainerAlreadyServingTheReleasesImageIsKeptRatherThanRecreated(t *testing.T) {
	t.Parallel()

	stand := stood(t, "true "+appImage)
	for _, command := range stand.commands() {
		if strings.Contains(command, quoted("run")+" "+quoted("--detach")) {
			t.Errorf("a redeploy of a release already standing ran %q, and the container serving live traffic is torn down for one that is the same", command)
		}
	}
}

func TestAContainerStandingUnderAnotherImageIsReplacedRatherThanLeftServing(t *testing.T) {
	t.Parallel()

	stand := stood(t, "true ocel/web:sha256-older")
	command := ranContainer(t, stand)
	if !strings.Contains(command, "docker rm --force "+quoted(physical)) && !strings.Contains(strings.Join(stand.commands(), "\n"), "docker rm --force "+quoted(physical)) {
		t.Errorf("the name was reused without taking the container holding it: %v", stand.commands())
	}
}

func TestTakingAContainerDownStopsItBeforeItIsRemoved(t *testing.T) {
	t.Parallel()

	stand := machine(nil)
	if err := stand.host().TakeDown(context.Background(), physical); err != nil {
		t.Fatalf("TakeDown() = %v", err)
	}
	joined := strings.Join(stand.commands(), "\n")
	stop, remove := strings.Index(joined, "docker stop"), strings.Index(joined, "docker rm")
	if stop < 0 || remove < 0 {
		t.Fatalf("taking a container down ran %v, want it stopped and removed", stand.commands())
	}
	if stop > remove {
		t.Error("a destroy removes the container before it stops it, and what it was serving is cut rather than closed")
	}
}

func TestTheInspectedStateNamesTheSevenFieldsItNeedsAndNothingTheContainerWasGiven(t *testing.T) {
	t.Parallel()

	command := stateCommand(physical)
	for _, field := range stateFields {
		if !strings.Contains(command, ".State."+field) {
			t.Errorf("the inspected state reads %q and never .State.%s: exited, running-with-no-answer and restarting are told apart by these and nothing else", command, field)
		}
	}
	for _, leak := range []string{".Config", ".Env", "{{json .}}", "{{.}}"} {
		if strings.Contains(command, leak) {
			t.Errorf("the inspected state reads %q, and %s prints the container's whole environment into a deploy's failure output", command, leak)
		}
	}
}

func TestAContainerNameIsDerivableAndDiffersBetweenReleases(t *testing.T) {
	t.Parallel()

	first := ContainerName("shop-prod", "web", "0123456789abcdef0123456789abcdef", appImage)
	if ContainerName("shop-prod", "web", "0123456789abcdef0123456789abcdef", appImage) != first {
		t.Error("two renders of one release name two containers, and nothing could then find the one it just started")
	}
	if second := ContainerName("shop-prod", "web", "fedcba9876543210fedcba9876543210", appImage); second == first {
		t.Errorf("two releases share the container name %q, and the drain's per-address count then attributes one release's requests to the other", second)
	}
	if unbuilt := ContainerName("shop-prod", "web", "", appImage); unbuilt == first || unbuilt == "" {
		t.Errorf("a release carrying no deployment id names its container %q", unbuilt)
	}
}
