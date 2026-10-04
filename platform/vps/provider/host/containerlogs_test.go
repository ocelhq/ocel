package host

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

var followSince = time.Date(2026, 3, 1, 10, 0, 0, 123, time.UTC)

type followedLine struct {
	container int
	line      Line
}

type followed struct {
	lines []followedLine
	gone  []int
	err   error
}

func followOn(t *testing.T, box *bench, names []string, lines ...session.Line) followed {
	t.Helper()
	box.followed = func(_ string, each func(session.Line) error) error {
		for _, line := range lines {
			if err := each(line); err != nil {
				return err
			}
		}
		return nil
	}
	var got followed
	got.err = box.host().FollowContainerLogs(context.Background(), names, followSince, func(container int, line Line) error {
		got.lines = append(got.lines, followedLine{container, line})
		return nil
	}, func(container int) error {
		got.gone = append(got.gone, container)
		return nil
	})
	return got
}

func TestFollowContainerLogsFollowsEveryContainerInOneCommand(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	got := followOn(t, box, []string{"shop-web-r1", "it's-odd"})
	if got.err != nil {
		t.Fatalf("FollowContainerLogs() error = %v", got.err)
	}
	var followers []string
	for _, command := range box.commands() {
		if strings.Contains(command, "docker logs") {
			followers = append(followers, command)
		}
	}
	if len(followers) != 1 {
		t.Fatalf("FollowContainerLogs ran %d commands that follow logs, want one for every container: %v", len(followers), followers)
	}
	for _, wanted := range []string{quoted("2026-03-01T10:00:00.000000123Z"), quoted("shop-web-r1"), quoted("it's-odd")} {
		if !strings.Contains(followers[0], wanted) {
			t.Errorf("FollowContainerLogs ran %q, want it to pass %s", followers[0], wanted)
		}
	}
}

func TestFollowContainerLogsGivesEachLineToTheContainerItCameFromAndKeepsItsStream(t *testing.T) {
	t.Parallel()

	got := followOn(t, machine(nil), []string{"web", "media"},
		session.Line{Pipe: session.Stdout, Text: "1 2026-03-01T10:00:01.000000002Z resized"},
		session.Line{Pipe: session.Stderr, Text: "0 2026-03-01T10:00:02Z warn: slow"},
		session.Line{Pipe: session.Stdout, Text: "0 2026-03-01T10:00:03Z served /"},
	)
	if got.err != nil {
		t.Fatalf("FollowContainerLogs() error = %v", got.err)
	}
	want := []followedLine{
		{1, Line{Time: time.Date(2026, 3, 1, 10, 0, 1, 2, time.UTC), Text: "resized"}},
		{0, Line{Time: time.Date(2026, 3, 1, 10, 0, 2, 0, time.UTC), Stderr: true, Text: "warn: slow"}},
		{0, Line{Time: time.Date(2026, 3, 1, 10, 0, 3, 0, time.UTC), Text: "served /"}},
	}
	if !slices.Equal(got.lines, want) {
		t.Errorf("FollowContainerLogs() delivered %+v, want %+v", got.lines, want)
	}
}

func TestFollowContainerLogsDropsWhatAFollowerRepeatsAfterItsContainerRestarts(t *testing.T) {
	t.Parallel()

	got := followOn(t, machine(nil), []string{"web"},
		session.Line{Pipe: session.Stdout, Text: "0 2026-03-01T10:00:01Z first run"},
		session.Line{Pipe: session.Stderr, Text: "0 2026-03-01T10:00:02Z first run warned"},
		session.Line{Pipe: session.Stdout, Text: "0 2026-03-01T10:00:01Z first run"},
		session.Line{Pipe: session.Stderr, Text: "0 2026-03-01T10:00:02Z first run warned"},
		session.Line{Pipe: session.Stdout, Text: "0 2026-03-01T10:00:05Z second run"},
	)
	var texts []string
	for _, line := range got.lines {
		texts = append(texts, line.line.Text)
	}
	if want := []string{"first run", "first run warned", "second run"}; !slices.Equal(texts, want) {
		t.Errorf("FollowContainerLogs() delivered %q, want %q: a follower that starts again replays lines already delivered", texts, want)
	}
}

func TestFollowContainerLogsDeliversNothingStampedBeforeSince(t *testing.T) {
	t.Parallel()

	got := followOn(t, machine(nil), []string{"web"},
		session.Line{Pipe: session.Stdout, Text: "0 2026-03-01T09:59:59Z early"},
		session.Line{Pipe: session.Stdout, Text: "0 2026-03-01T10:00:01Z on time"},
	)
	if len(got.lines) != 1 || got.lines[0].line.Text != "on time" {
		t.Errorf("FollowContainerLogs() delivered %+v, want only the line stamped from since on", got.lines)
	}
}

func TestFollowContainerLogsSkipsAnOutputLineWithoutATimestamp(t *testing.T) {
	t.Parallel()

	got := followOn(t, machine(nil), []string{"web"},
		session.Line{Pipe: session.Stdout, Text: "0 no stamp here"},
		session.Line{Pipe: session.Stdout, Text: "untagged"},
		session.Line{Pipe: session.Stdout, Text: "0 2026-03-01T10:00:01Z kept"},
	)
	if got.err != nil || len(got.lines) != 1 || got.lines[0].line.Text != "kept" {
		t.Errorf("FollowContainerLogs() = %+v, %v, want only the stamped line", got.lines, got.err)
	}
}

func TestFollowContainerLogsReportsAContainerThatIsGoneOnceAndFollowsTheRest(t *testing.T) {
	t.Parallel()

	got := followOn(t, machine(nil), []string{"web", "media"},
		session.Line{Pipe: session.Stderr, Text: "0 Error response from daemon: No such container: web"},
		session.Line{Pipe: session.Stderr, Text: "0 Error response from daemon: No such container: web"},
		session.Line{Pipe: session.Stdout, Text: "1 2026-03-01T10:00:01Z still here"},
	)
	if got.err != nil {
		t.Fatalf("FollowContainerLogs() error = %v, want a container that is gone reported and the rest followed", got.err)
	}
	if !slices.Equal(got.gone, []int{0}) {
		t.Errorf("FollowContainerLogs() reported %v gone, want the first container once", got.gone)
	}
	if len(got.lines) != 1 || got.lines[0].container != 1 {
		t.Errorf("FollowContainerLogs() delivered %+v, want the surviving container's line", got.lines)
	}
}

func TestFollowContainerLogsFailsWhenDockerCannotFollowAContainer(t *testing.T) {
	t.Parallel()

	got := followOn(t, machine(nil), []string{"web"},
		session.Line{Pipe: session.Stderr, Text: "0 Cannot connect to the Docker daemon at unix:///var/run/docker.sock"},
	)
	if got.err == nil || !strings.Contains(got.err.Error(), "Cannot connect to the Docker daemon") || !strings.Contains(got.err.Error(), "web") {
		t.Errorf("FollowContainerLogs() error = %v, want docker's own words and the container", got.err)
	}
}

func TestFollowContainerLogsFailsWhenTheBoxSaysSomethingOfItsOwn(t *testing.T) {
	t.Parallel()

	got := followOn(t, machine(nil), []string{"web"},
		session.Line{Pipe: session.Stderr, Text: "sh: awk: not found"},
	)
	if got.err == nil || !strings.Contains(got.err.Error(), "awk: not found") {
		t.Errorf("FollowContainerLogs() error = %v, want what the box said", got.err)
	}
}

func TestFollowContainerLogsStopsWhenTheCallerRefusesALine(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	box.followed = func(_ string, each func(session.Line) error) error {
		return each(session.Line{Text: "0 2026-03-01T10:00:01Z one"})
	}
	stop := errors.New("the caller has had enough")
	err := box.host().FollowContainerLogs(context.Background(), []string{"web"}, followSince, func(int, Line) error { return stop }, func(int) error { return nil })
	if !errors.Is(err, stop) {
		t.Errorf("FollowContainerLogs() error = %v, want the caller's error", err)
	}
}

func TestFollowContainerLogsReachesTheDaemonAsRootForALoginOutsideTheDockerGroup(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	box.facts = session.Facts{Systemd: true}
	box.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker") && !strings.HasPrefix(command, "sudo -n ") {
			return session.Result{Code: 1, Stderr: "permission denied while trying to connect to the Docker daemon socket"}, true
		}
		return session.Result{}, false
	}
	if got := followOn(t, box, []string{"web"}); got.err != nil {
		t.Fatalf("FollowContainerLogs() error = %v", got.err)
	}
	command := box.commands()[box.at("docker logs")]
	if !strings.HasPrefix(command, "sudo -n ") {
		t.Errorf("FollowContainerLogs ran %q, want it as root", command)
	}
}
