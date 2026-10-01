package kv_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	"github.com/ocelhq/ocel/cli/internal/devresources/kv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

const liveEnv = "OCEL_LIVE_DOCKER"

func TestDockerDeclaredStoresComeUpBehindTheirUserAndKeepTheirDataAcrossRuns(t *testing.T) {
	if os.Getenv(liveEnv) == "" {
		t.Skipf("no docker daemon promised to this run; set %s=1 where one is running", liveEnv)
	}
	ctx := context.Background()
	const project = "kv-live-test"
	engine, err := docker.Open(ctx)
	if err != nil {
		t.Fatalf("Open = %v", err)
	}
	t.Cleanup(func() {
		_ = engine.Wipe(ctx, docker.ProjectLabels(project))
		_ = engine.Close()
	})
	state := t.TempDir()
	store := []declaration.Resource{declared("cache", &resourcesv1.KvConfig{Eviction: "allkeys-lru", Memory: "64mb"})}

	first := kv.New(docker.Open, state)
	resolved, err := first.Resolve(ctx, project, store)
	if err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	props := bound(t, resolved[0].Env["OCEL_RESOURCE_KV_cache"])
	if said := converse(t, props, "", "PING"); !strings.HasPrefix(said, "-NOAUTH") {
		t.Errorf("an unauthenticated PING got %q, want the default user off", said)
	}
	if said := converse(t, props, "WRONG", "PING"); !strings.HasPrefix(said, "-WRONGPASS") {
		t.Errorf("a wrong password got %q, want it refused", said)
	}
	if said := converse(t, props, props.GetPassword(), "SET kept yes"); said != "+OK" {
		t.Fatalf("SET as the bound user = %q", said)
	}
	if said := converse(t, props, props.GetPassword(), "CONFIG GET maxmemory-policy"); !strings.Contains(said, "allkeys-lru") {
		t.Errorf("maxmemory-policy = %q, want the declared eviction", said)
	}
	if err := first.Close(ctx, true); err != nil {
		t.Fatalf("Close = %v", err)
	}

	second := kv.New(docker.Open, state)
	t.Cleanup(func() { _ = second.Close(ctx, true) })
	again, err := second.Resolve(ctx, project, store)
	if err != nil {
		t.Fatalf("Resolve on a second run = %v", err)
	}
	props = bound(t, again[0].Env["OCEL_RESOURCE_KV_cache"])
	if said := converse(t, props, props.GetPassword(), "GET kept"); said != "yes" {
		t.Errorf("GET kept after a restart = %q, want the data kept on its volume", said)
	}
}

func converse(t *testing.T, props *bindingsv1.KvProperties, password, command string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(props.GetHost(), strconv.Itoa(int(props.GetPort()))), time.Second)
	if err != nil {
		t.Fatalf("nothing answers where the binding points: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(conn)
	if password != "" {
		fmt.Fprintf(conn, "AUTH %s %s\r\n", props.GetUsername(), password)
		if line, _ := reader.ReadString('\n'); !strings.HasPrefix(line, "+OK") {
			return strings.TrimSpace(line)
		}
	}
	fmt.Fprintf(conn, "%s\r\n", command)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read the reply to %s: %v", command, err)
	}
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "$") {
		body, _ := reader.ReadString('\n')
		return strings.TrimSpace(body)
	}
	if strings.HasPrefix(line, "*") {
		var parts []string
		count, _ := strconv.Atoi(line[1:])
		for range count {
			_, _ = reader.ReadString('\n')
			body, _ := reader.ReadString('\n')
			parts = append(parts, strings.TrimSpace(body))
		}
		return strings.Join(parts, " ")
	}
	return line
}
