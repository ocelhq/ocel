package enginetest

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/images"
)

type QueueDatabase struct {
	Name string
	URL  string
}

var (
	queueDatabase onceOrSkip[QueueDatabase]
	databases     = taken{by: map[string]string{}}
)

func SharedQueueDatabase(t *testing.T) QueueDatabase {
	t.Helper()
	labels := RunLabelArgs(t)
	return queueDatabase.get(t, func() (QueueDatabase, string) { return startQueueDatabase(labels, 90*time.Second) })
}

func (s QueueDatabase) ClaimDatabase(t *testing.T, database string) {
	t.Helper()
	if err := databases.take(database, t.Name()); err != nil {
		t.Fatal(err)
	}
}

func startQueueDatabase(labels []string, patience time.Duration) (QueueDatabase, string) {
	started := QueueDatabase{Name: uniqueName("queue-database")}
	argv := append([]string{"run", "--detach", "--name", started.Name, "--publish", "127.0.0.1::5432"}, labels...)
	argv = append(argv, "--env", "POSTGRES_PASSWORD=probe-secret", images.QueueDatabase())
	if said, err := exec.Command(engine, argv...).CombinedOutput(); err != nil {
		return QueueDatabase{}, fmt.Sprintf("no queue database to drive: %v\n%s", err, said)
	}
	published, err := exec.Command(engine, "port", started.Name, "5432/tcp").Output()
	if err != nil {
		return QueueDatabase{}, fmt.Sprintf("read the port the queue database publishes: %v", err)
	}
	started.URL = "postgres://postgres:probe-secret@" + strings.TrimSpace(strings.Split(string(published), "\n")[0]) + "/postgres?sslmode=disable"

	deadline := time.Now().Add(patience)
	for {
		said, err := exec.Command(engine, "exec", started.Name, "pg_isready", "--host", "127.0.0.1", "--username", "postgres").CombinedOutput()
		if err == nil {
			return started, ""
		}
		if time.Now().After(deadline) {
			return QueueDatabase{}, fmt.Sprintf("the queue database never took a connection over TCP: %v\n%s", err, said)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
