package host

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

type bench struct {
	mu     sync.Mutex
	dest   session.Destination
	facts  session.Facts
	stands map[providerkit.Class][]Item
	ran    []string
	dials  int
	dead   error
	answer func(command string) (session.Result, bool)
	after  func(b *bench, command string)
}

func machine(stands map[providerkit.Class][]Item) *bench {
	return &bench{
		dest: session.Destination{
			Written:    "ocelbox",
			Address:    "203.0.113.10",
			Port:       2222,
			User:       "ada",
			KnownHosts: []string{"/home/ada/.ssh/known_hosts"},
		},
		facts:  session.Facts{Root: true, Systemd: true, Arch: "x86_64"},
		stands: stands,
	}
}

func (b *bench) host() *Host { return New(b.dial, Keys{}) }

func (b *bench) dial(context.Context) (Conn, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dials++
	if b.dead != nil {
		return nil, b.dead
	}
	return wire{b}, nil
}

func (b *bench) commands() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.ran...)
}

func (b *bench) taking() []string {
	var taken []string
	for _, command := range b.commands() {
		if strings.HasPrefix(command, "rm ") || strings.HasPrefix(command, "rm -rf ") ||
			strings.HasPrefix(command, "rmdir ") || strings.HasPrefix(command, "userdel ") ||
			strings.HasPrefix(command, "docker ") || strings.HasPrefix(command, "if ! docker ") {
			taken = append(taken, command)
		}
	}
	return taken
}

func (b *bench) took(fragment string) int {
	for at, command := range b.taking() {
		if strings.Contains(command, fragment) {
			return at
		}
	}
	return -1
}

type wire struct{ b *bench }

func (w wire) Destination() session.Destination { return w.b.dest }

func (w wire) Preflight(context.Context) (session.Facts, error) { return w.b.facts, nil }

func (w wire) Run(ctx context.Context, command string) (string, error) {
	result, err := w.Exec(ctx, command, nil)
	if err != nil {
		return "", err
	}
	if result.Code != 0 {
		return "", errors.New(result.Stderr)
	}
	return result.Stdout, nil
}

func (w wire) Exec(_ context.Context, command string, _ []byte) (session.Result, error) {
	b := w.b
	b.mu.Lock()
	b.ran = append(b.ran, command)
	answer, hook := b.answer, b.after
	b.mu.Unlock()

	result := b.rendered(command)
	if answer != nil {
		if scripted, said := answer(command); said {
			result = scripted
		}
	}
	if hook != nil {
		hook(b, command)
	}
	return result, nil
}

func (b *bench) rendered(command string) session.Result {
	switch {
	case strings.Contains(command, "id -u"):
		return session.Result{Stdout: "0\n"}
	case strings.Contains(command, "for p in"):
		for class, items := range b.stands {
			if strings.Contains(command, quoted(StampPath(class))) {
				return session.Result{Stdout: surveyed(items)}
			}
		}
		return session.Result{}
	case strings.HasPrefix(command, "cat "):
		for _, items := range b.stands {
			for _, item := range items {
				if command == "cat "+quoted(item.Name) {
					return session.Result{Stdout: string(item.Content)}
				}
			}
		}
		return session.Result{Code: 1, Stderr: "no such file or directory"}
	default:
		return session.Result{}
	}
}

func surveyed(items []Item) string {
	var rendered strings.Builder
	for _, item := range items {
		fmt.Fprintf(&rendered, "%s\t%s\t%o\t%s\t%s", item.Kind, item.Name, item.Mode, item.Owner, item.sum())
		if item.Kind == KindSealKey {
			rendered.WriteString("\t2026-01-01T00:00:00Z")
		}
		rendered.WriteString("\n")
	}
	return rendered.String()
}

func bootstrapped(t *testing.T, class providerkit.Class) []Item {
	t.Helper()
	stamp, err := Stamp{Schema: providerkit.BootstrapSchema, State: StateComplete}.item(class)
	if err != nil {
		t.Fatal(err)
	}
	return append(Items(class, []byte(aKey+"\n"), ArchAMD64), stamp)
}

func TestASurveyedHostReadsBackAsTheItemsThatStandOnIt(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	items := Items(class, []byte(aKey+"\n"), ArchAMD64)
	stood := machine(map[providerkit.Class][]Item{class: items})

	read, err := stood.host().Survey(context.Background(), class)
	if err != nil {
		t.Fatalf("Survey() = %v", err)
	}
	for _, item := range items {
		if !read.current(item) {
			t.Errorf("%s surveys as absent or moved, so nothing this bench proves about a standing host means anything", item.ID())
		}
	}
}
