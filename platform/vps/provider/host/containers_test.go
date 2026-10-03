package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
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

func imaging(b *bench, served string) {
	b.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker inspect") && strings.Contains(command, quoted(servingSelectors())) {
			return session.Result{Stdout: served + "\n"}, true
		}
		return session.Result{}, false
	}
}

func runningContainer(t *testing.T, served string) *bench {
	t.Helper()
	box := machine(nil)
	imaging(box, served)
	if err := box.host().RunContainer(context.Background(), aContainer()); err != nil {
		t.Fatalf("RunContainer() = %v", err)
	}
	return box
}

func ranContainer(t *testing.T, box *bench) string {
	t.Helper()
	for _, command := range box.commands() {
		if strings.Contains(command, quoted("run")+" "+quoted("--detach")) {
			return command
		}
	}
	t.Fatalf("nothing ran a container: %v", box.commands())
	return ""
}

func TestAReleaseRunsOneLabelledContainerOnTheOneNetworkTargetsResolveAcross(t *testing.T) {
	t.Parallel()

	command := ranContainer(t, runningContainer(t, "false "))
	for what, wanted := range map[string]string{
		"the name the drain attributes its in-flight count to": quoted("--name") + " " + quoted(physical),
		"a reboot that does not take the app down":             quoted("--restart") + " " + quoted(appRestart),
		"the network the proxy reaches it over":                quoted("--network") + " " + quoted(live.AppNetwork(aContainer().Tier, "shop")),
		"the app label retention reads":                        quoted("--label") + " " + quoted(LabelApp+"=web"),
		"the project label retention reads":                    quoted("--label") + " " + quoted(LabelProject+"=shop"),
		"the ref label retention reads":                        quoted("--label") + " " + quoted(LabelRef+"="+appImage),
		"the port the app is told to bind":                     quoted("--env") + " " + quoted("PORT="+containerimage.PortText),
		"the image the release names":                          quoted(appImage),
	} {
		if !strings.Contains(command, wanted) {
			t.Errorf("starting a container runs %q, which contains no %s (%s)", command, what, wanted)
		}
	}
}

func TestNoPortIsPublishedForAnAppContainerAndNoListenerIsAddedAnywhere(t *testing.T) {
	t.Parallel()

	command := ranContainer(t, runningContainer(t, "false "))
	for _, opening := range []string{quoted("--publish"), quoted("-p"), "--network host", quoted("--expose")} {
		if strings.Contains(command, opening) {
			t.Errorf("starting a container runs %q, and %q puts the app on an address the proxy is not the only way to: every probe and every request reaches it over the shared network alone",
				command, opening)
		}
	}
}

func TestAContainerAlreadyServingTheReleasesImageIsKeptRatherThanRecreated(t *testing.T) {
	t.Parallel()

	box := runningContainer(t, "running "+appImage+" "+handedTo(aContainer()).digest)
	for _, command := range box.commands() {
		if strings.Contains(command, quoted("run")+" "+quoted("--detach")) {
			t.Errorf("a redeploy of a release already running ran %q, and the container serving live traffic is torn down for one that is the same", command)
		}
	}
}

func TestAContainerOfAnotherImageIsReplacedRatherThanLeftServing(t *testing.T) {
	t.Parallel()

	box := runningContainer(t, "running ocel/shop/web:sha256-older")
	command := ranContainer(t, box)
	if !strings.Contains(command, "docker rm --force "+quoted(physical)) && !strings.Contains(strings.Join(box.commands(), "\n"), "docker rm --force "+quoted(physical)) {
		t.Errorf("the name was reused without taking the container that owns it: %v", box.commands())
	}
}

func TestTakingAContainerDownStopsItBeforeItIsRemoved(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	if err := box.host().TakeDown(context.Background(), environment.TierProduction, physical); err != nil {
		t.Fatalf("TakeDown() = %v", err)
	}
	joined := strings.Join(box.commands(), "\n")
	stop, remove := strings.Index(joined, "docker stop"), strings.Index(joined, "docker rm")
	if stop < 0 || remove < 0 {
		t.Fatalf("taking a container down ran %v, want it stopped and removed", box.commands())
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
	var containers []map[string]any
	if err := json.Unmarshal(read, &containers); err != nil {
		t.Fatal(err)
	}
	if len(containers) != 1 {
		t.Fatalf("testdata/inspect.json contains %d containers, want the one a daemon answers --type container with", len(containers))
	}
	return containers[0]
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
		t.Errorf("a container running under %q and the values it was handed reads as replaceable", said)
	}
	if stillServing("running "+fixtureRef+" "+fields[2], fixtureRef, "0000000000") {
		t.Error("a container running under values a deploy has since changed reads as still serving, and it would keep the old ones for the life of the release")
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
		t.Errorf("a release with no deployment id names its container %q", unbuilt)
	}
}

func TestAContainerReadingValuesLiveIsHandedTheBoxSocketReadOnlyAndItsManifestByFile(t *testing.T) {
	t.Parallel()

	spec := valued()
	box := runningWith(t, spec)
	command := ranContainer(t, box)
	mount := quoted("--mount") + " " + quoted("type=bind,src="+LiveSocketDir+",dst="+LiveSocketDir+",readonly")
	if !strings.Contains(command, mount) {
		t.Errorf("starting a live container runs %q, which hands it no socket to read its values through (%s)", command, mount)
	}
	tmpfs := quoted("--tmpfs") + " " + quoted(containerimage.LivePath+":rw,noexec,nosuid,size=8m")
	if !strings.Contains(command, tmpfs) {
		t.Errorf("starting a live container runs %q, which gives the runtime nowhere in memory to project the values into (%s): an image built from scratch has no /tmp, and the writable layer is the box's disk", command, tmpfs)
	}
	if strings.Contains(command, aManifest) {
		t.Errorf("the command line contains the manifest, which every login reads out of `ps`; it travels in the env file")
	}
	file := wrote(t, box, EnvFile(spec.Tier, spec.Name))
	for _, want := range []string{"OCEL_LIVE_MANIFEST=" + aManifest, "OCEL_HEALTH_PATH=/healthz", "API_TOKEN=" + sensitiveValue, "REGION=eu-west-1"} {
		if !strings.Contains(file, want) {
			t.Errorf("the env file reads %q and never binds %s", file, want)
		}
	}
	if strings.Contains(file, "DATABASE_URL=") || strings.Contains(file, "OCEL_RESOURCE_POSTGRES_main=") {
		t.Errorf("the env file reads %q and contains a value the container reads live", file)
	}

	baked := spec
	baked.Manifest = nil
	bakedBox := runningWith(t, baked)
	if command := ranContainer(t, bakedBox); strings.Contains(command, quoted("--mount")) || strings.Contains(command, quoted("--tmpfs")) {
		t.Errorf("a container reading nothing live runs %q and is handed the socket or the projection anyway", command)
	}
	if file := wrote(t, bakedBox, EnvFile(baked.Tier, baked.Name)); strings.Contains(file, "OCEL_LIVE_MANIFEST") {
		t.Errorf("a container reading nothing live is handed %q, which names a manifest", file)
	}
}

func TestAChangedManifestReplacesTheContainerLikeAChangedValue(t *testing.T) {
	t.Parallel()

	spec := valued()
	current, err := handing(spec)
	if err != nil {
		t.Fatal(err)
	}
	moved := spec
	moved.Manifest = []byte(`{"slug":"shop","tier":"production","keys":[{"key":"DATABASE_URL"},{"key":"SESSION"}]}`)
	other, err := handing(moved)
	if err != nil {
		t.Fatal(err)
	}
	if other.digest == current.digest {
		t.Error("a deploy that declares one more live key is labelled the same as the one before it, and the container running under the old manifest would never read the new key")
	}
}

func readingLogs(t *testing.T, answer session.Result) (*bench, []Line, error) {
	t.Helper()
	box := machine(nil)
	box.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker logs") {
			return answer, true
		}
		return session.Result{}, false
	}
	since := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	lines, err := box.host().ReadContainerLogs(context.Background(), physical, since, time.Time{}, 50, "")
	return box, lines, err
}

func TestReadContainerLogsKeepsStdoutAndStderrApartAndOrdersThemByTime(t *testing.T) {
	t.Parallel()

	box, lines, err := readingLogs(t, session.Result{
		Stdout: "2026-03-01T10:00:01.000000002Z listening on 3000\n2026-03-01T10:00:03Z served /\n",
		Stderr: "2026-03-01T10:00:02.5Z warn: slow query\n",
	})
	if err != nil {
		t.Fatalf("ReadContainerLogs() error = %v", err)
	}
	want := []Line{
		{Time: time.Date(2026, 3, 1, 10, 0, 1, 2, time.UTC), Text: "listening on 3000"},
		{Time: time.Date(2026, 3, 1, 10, 0, 2, 500_000_000, time.UTC), Stderr: true, Text: "warn: slow query"},
		{Time: time.Date(2026, 3, 1, 10, 0, 3, 0, time.UTC), Text: "served /"},
	}
	if !slices.Equal(lines, want) {
		t.Errorf("ReadContainerLogs() = %+v, want %+v", lines, want)
	}
	command := box.commands()[box.at("docker logs")]
	if strings.Contains(command, "2>&1") {
		t.Errorf("ReadContainerLogs ran %q, and 2>&1 merges the stderr it must keep apart", command)
	}
	for _, wanted := range []string{"--timestamps", "--since " + quoted("2026-03-01T10:00:00Z"), "--tail 50", quoted(physical)} {
		if !strings.Contains(command, wanted) {
			t.Errorf("ReadContainerLogs ran %q, want it to contain %q", command, wanted)
		}
	}
	if strings.Contains(command, "--until") {
		t.Errorf("ReadContainerLogs ran %q with no until given, want no --until", command)
	}
}

func TestReadContainerLogsBoundsTheReadByUntilWhenGiven(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	until := time.Date(2026, 3, 1, 11, 0, 0, 0, time.UTC)
	if _, err := box.host().ReadContainerLogs(context.Background(), physical, until.Add(-time.Hour), until, 5, ""); err != nil {
		t.Fatalf("ReadContainerLogs() error = %v", err)
	}
	if command := box.commands()[box.at("docker logs")]; !strings.Contains(command, "--until "+quoted("2026-03-01T11:00:00Z")) {
		t.Errorf("ReadContainerLogs ran %q, want it bounded by --until", command)
	}
}

func TestReadContainerLogsPassesSinceAndUntilToTheNanosecond(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	since := time.Date(2026, 3, 1, 10, 0, 0, 500_000_001, time.UTC)
	until := time.Date(2026, 3, 1, 11, 0, 0, 999_999_999, time.UTC)
	if _, err := box.host().ReadContainerLogs(context.Background(), physical, since, until, 5, ""); err != nil {
		t.Fatalf("ReadContainerLogs() error = %v", err)
	}
	command := box.commands()[box.at("docker logs")]
	for _, wanted := range []string{"--since " + quoted("2026-03-01T10:00:00.500000001Z"), "--until " + quoted("2026-03-01T11:00:00.999999999Z")} {
		if !strings.Contains(command, wanted) {
			t.Errorf("ReadContainerLogs ran %q, want it to contain %q", command, wanted)
		}
	}
}

func readingLogsBetween(t *testing.T, stdout string, since, until time.Time, limit int, contains string) (string, []string) {
	t.Helper()
	box := machine(nil)
	box.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker logs") {
			return session.Result{Stdout: stdout}, true
		}
		return session.Result{}, false
	}
	lines, err := box.host().ReadContainerLogs(context.Background(), physical, since, until, limit, contains)
	if err != nil {
		t.Fatalf("ReadContainerLogs() error = %v", err)
	}
	var texts []string
	for _, line := range lines {
		texts = append(texts, line.Text)
	}
	return box.commands()[box.at("docker logs")], texts
}

const windowedLogs = "2026-03-01T09:59:59.9Z before\n" +
	"2026-03-01T10:00:00Z first\n" +
	"2026-03-01T10:00:01Z boom second\n" +
	"2026-03-01T10:00:02Z third\n" +
	"2026-03-01T10:00:03Z boom fourth\n" +
	"2026-03-01T10:00:04Z fifth\n" +
	"2026-03-01T10:00:04.1Z after\n"

func TestReadContainerLogsKeepsTheNewestLimitLinesOfAWindowThatEndsBeforeTheLog(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	command, got := readingLogsBetween(t, windowedLogs, since, since.Add(4*time.Second), 2, "")
	if want := []string{"boom fourth", "fifth"}; !slices.Equal(got, want) {
		t.Errorf("ReadContainerLogs() = %v, want %v", got, want)
	}
	if strings.Contains(command, "--tail") {
		t.Errorf("ReadContainerLogs ran %q, and docker takes the tail before it applies --until, which would drop the window's newest lines", command)
	}
}

func TestReadContainerLogsKeepsTheNewestLimitLinesThatContainTheText(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	command, got := readingLogsBetween(t, windowedLogs, since, time.Time{}, 1, "boom")
	if want := []string{"boom fourth"}; !slices.Equal(got, want) {
		t.Errorf("ReadContainerLogs() = %v, want %v", got, want)
	}
	if strings.Contains(command, "--tail") {
		t.Errorf("ReadContainerLogs ran %q, and a tail taken before the text is matched leaves fewer matches than there are", command)
	}
}

func TestReadContainerLogsDropsLinesDockerReturnsOutsideTheWindow(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	_, got := readingLogsBetween(t, windowedLogs, since, since.Add(4*time.Second), 10, "")
	if want := []string{"first", "boom second", "third", "boom fourth", "fifth"}; !slices.Equal(got, want) {
		t.Errorf("ReadContainerLogs() = %v, want %v", got, want)
	}
}

func TestReadContainerLogsNamesAContainerThatNoLongerExists(t *testing.T) {
	t.Parallel()

	_, lines, err := readingLogs(t, session.Result{Code: 1, Stderr: "Error response from daemon: No such container: " + physical})
	if !errors.Is(err, ErrContainerMissing) {
		t.Errorf("ReadContainerLogs() error = %v, want ErrContainerMissing", err)
	}
	if len(lines) != 0 {
		t.Errorf("ReadContainerLogs() = %v, want no lines", lines)
	}
}

func TestReadContainerLogsRefusesAnyOtherDockerFailure(t *testing.T) {
	t.Parallel()

	_, _, err := readingLogs(t, session.Result{Code: 1, Stderr: "Cannot connect to the Docker daemon"})
	if err == nil || errors.Is(err, ErrContainerMissing) {
		t.Errorf("ReadContainerLogs() error = %v, want a failure that is not ErrContainerMissing", err)
	}
}
