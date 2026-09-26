package enginetest

import (
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/constants"
)

type ObjectStore struct {
	Name        string
	Endpoint    string
	Inside      string
	Region      string
	AccessKeyID string
	SecretKey   string
}

var (
	store   onceOrSkip[ObjectStore]
	buckets = taken{by: map[string]string{}}
)

func SharedObjectStore(t *testing.T) ObjectStore {
	t.Helper()
	labels := RunLabelArgs(t)
	return store.get(t, func() (ObjectStore, string) { return startObjectStore(labels, 90*time.Second) })
}

func (s ObjectStore) ClaimBucket(t *testing.T, bucket string) {
	t.Helper()
	if err := buckets.take(bucket, t.Name()); err != nil {
		t.Fatal(err)
	}
}

type taken struct {
	mu sync.Mutex
	by map[string]string
}

func (k *taken) take(name, by string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if holder, held := k.by[name]; held && holder != by {
		return fmt.Errorf("%s already took %q on the store this run shares, and a test that reads what another wrote proves nothing about its own", holder, name)
	}
	k.by[name] = by
	return nil
}

func startObjectStore(labels []string, patience time.Duration) (ObjectStore, string) {
	started := ObjectStore{
		Name:        uniqueName("store"),
		Inside:      "http://127.0.0.1:9000",
		Region:      "us-east-1",
		AccessKeyID: "ocel",
		SecretKey:   "probe-secret",
	}
	argv := append([]string{"run", "--detach", "--name", started.Name, "--publish", "127.0.0.1::9000"}, labels...)
	argv = append(argv,
		"--env", "RUSTFS_ACCESS_KEY="+started.AccessKeyID,
		"--env", "RUSTFS_SECRET_KEY="+started.SecretKey,
		"--env", "RUSTFS_ADDRESS=:9000",
		"--env", "RUSTFS_CONSOLE_ENABLE=false",
		"--env", "RUSTFS_REGION="+started.Region,
		"--env", "RUSTFS_VOLUMES=/data",
		constants.ObjectStoreImage())
	if said, err := exec.Command(engine, argv...).CombinedOutput(); err != nil {
		return ObjectStore{}, fmt.Sprintf("no store to drive: %v\n%s", err, said)
	}
	published, err := exec.Command(engine, "port", started.Name, "9000/tcp").Output()
	if err != nil {
		return ObjectStore{}, fmt.Sprintf("read the port the store publishes: %v", err)
	}
	started.Endpoint = "http://" + strings.TrimSpace(strings.Split(string(published), "\n")[0])

	deadline := time.Now().Add(patience)
	for {
		if said, err := http.Get(started.Endpoint + "/health/ready"); err == nil {
			said.Body.Close()
			if said.StatusCode == http.StatusOK {
				return started, ""
			}
		}
		if time.Now().After(deadline) {
			return ObjectStore{}, "the store never answered its readiness probe"
		}
		time.Sleep(time.Second)
	}
}
