package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const (
	appImage   = "ocel/shop/web:sha256-abc"
	physical   = "shop-web-abc123def456"
	fixtureRef = "registry.example.com/web@sha256:0000"
)

func aContainer() Container {
	return Container{Name: physical, Project: "shop", App: "web", Image: appImage, Resolved: true}
}

func imaging(b *bench, held string) {
	b.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker inspect") && strings.Contains(command, quoted(servingSelectors())) {
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
		"the project label retention reads":                    quoted("--label") + " " + quoted(LabelProject+"=shop"),
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

	stand := stood(t, "running "+appImage+" "+handedTo(aContainer()).digest)
	for _, command := range stand.commands() {
		if strings.Contains(command, quoted("run")+" "+quoted("--detach")) {
			t.Errorf("a redeploy of a release already standing ran %q, and the container serving live traffic is torn down for one that is the same", command)
		}
	}
}

func TestAContainerStandingUnderAnotherImageIsReplacedRatherThanLeftServing(t *testing.T) {
	t.Parallel()

	stand := stood(t, "running ocel/shop/web:sha256-older")
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

func inspected(t *testing.T) map[string]any {
	t.Helper()
	read, err := os.ReadFile(filepath.Join("testdata", "inspect.json"))
	if err != nil {
		t.Fatal(err)
	}
	var held []map[string]any
	if err := json.Unmarshal(read, &held); err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 {
		t.Fatalf("testdata/inspect.json holds %d containers, want the one a daemon answers --type container with", len(held))
	}
	return held[0]
}

func rendering(t *testing.T, format string) string {
	t.Helper()
	parsed, err := template.New("").Option("missingkey=error").Parse(format)
	if err != nil {
		t.Fatalf("docker inspect --format %q does not parse: %v", format, err)
	}
	var written strings.Builder
	if err := parsed.Execute(&written, inspected(t)); err != nil {
		t.Fatalf("docker inspect --format %q against what a daemon really answers: %v", format, err)
	}
	return written.String()
}

func TestTheInspectedStateNamesTheSevenFieldsItNeedsAndNothingTheContainerWasGiven(t *testing.T) {
	t.Parallel()

	said := rendering(t, strings.Join(stateSelectors(), " "))
	for _, field := range stateFields {
		if !strings.Contains(said, field.label+"=") {
			t.Errorf("the inspected state of a real container reads %q and never %s: exited, running-with-no-answer and restarting are told apart by these and nothing else", said, field.label)
		}
	}
	command := stateCommand(physical)
	for _, leak := range []string{".Config", ".Env", "{{json .}}", "{{.}}"} {
		if strings.Contains(command, leak) {
			t.Errorf("the inspected state reads %q, and %s prints the container's whole environment into a deploy's failure output", command, leak)
		}
	}
}

func TestTheStateLineOfACrashLoopingContainerCountsItsRestarts(t *testing.T) {
	t.Parallel()

	said := rendering(t, strings.Join(stateSelectors(), " "))
	for _, wanted := range []string{"Status=restarting", "ExitCode=3", "OOMKilled=false", "RestartCount=6"} {
		if !strings.Contains(said, wanted) {
			t.Errorf("a daemon's crash-looping container renders as %q, which never says %s: a restart policy makes the loop invisible without it", said, wanted)
		}
	}
	if strings.Contains(said, "RestartCount=0") {
		t.Errorf("a container a daemon has restarted six times renders as %q", said)
	}
}

func TestWhatIsAlreadyServingIsReadFromWhereADaemonKeepsIt(t *testing.T) {
	t.Parallel()

	if !strings.Contains(servingCommand(physical), quoted(servingSelectors())) {
		t.Fatalf("the serving check reads %q and no longer asks the daemon for the format it reads", servingCommand(physical))
	}
	said := rendering(t, servingSelectors())
	fields := strings.Fields(said)
	if len(fields) != 3 || fields[1] != fixtureRef {
		t.Errorf("a container labelled %s reads as %q, and a redeploy of the image already up would tear it down for the same one", fixtureRef, said)
	}
	if strings.Contains(said, "<no value>") {
		t.Errorf("a real container reads as %q: a selector that will not render answers with a word no digest and no ref equals, and every redeploy would then replace a container serving live traffic", said)
	}
	if fields[0] == "running" {
		t.Errorf("a crash-looping container reads as %q, and a redeploy is kept off a container that serves nothing", said)
	}
	if !stillServing("running "+fixtureRef+" "+fields[2], fixtureRef, fields[2]) {
		t.Errorf("a container standing under %q and the values it was handed reads as replaceable", said)
	}
	if stillServing("running "+fixtureRef+" "+fields[2], fixtureRef, "0000000000") {
		t.Error("a container standing under values a deploy has since changed reads as still serving, and it would keep the old ones for the life of the release")
	}
}

func TestTheLabelsRetentionReadsRenderAgainstWhatADaemonAnswersRatherThanFailingIntoEmpty(t *testing.T) {
	t.Parallel()

	for label, wanted := range map[string]string{LabelApp: "web", LabelRef: fixtureRef} {
		if said := rendering(t, LabelSelector(label)); said != wanted {
			t.Errorf("a real container's %s reads as %q, want %q: a selector that will not parse is answered as the empty string, and retention would then take the image out from under a container still serving it",
				label, said, wanted)
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
