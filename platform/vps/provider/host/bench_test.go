package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

type bench struct {
	mu     sync.Mutex
	dest   session.Destination
	facts  session.Facts
	stands map[providerkit.Class][]Item
	ran    []string
	fed    []string
	dials  int
	dead   error
	floor  error
	answer func(command string) (session.Result, bool)
	broke  func(command string) error
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

func (b *bench) host() *Host { return New(b.dial, Keys{}, nil) }

type recorder struct{ said *[]string }

func saying(said *[]string) providerkit.Reporter { return recorder{said: said} }

func (r recorder) Say(message string) { *r.said = append(*r.said, message) }

func (recorder) Detail(string) {}

func (recorder) Span(string, time.Time, time.Time, error, ...edge.Attr) {}

func (b *bench) dial(ctx context.Context) (Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dials++
	if b.dead != nil {
		return nil, b.dead
	}
	return wire{b}, nil
}

func drained(stdin io.Reader) (string, error) {
	if stdin == nil {
		return "", nil
	}
	raw, err := io.ReadAll(stdin)
	return string(raw), err
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

func (b *bench) at(fragment string) int {
	for at, command := range b.commands() {
		if strings.Contains(command, fragment) {
			return at
		}
	}
	return -1
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

func (w wire) Preflight(context.Context) (session.Facts, error) { return w.b.facts, w.b.floor }

func (w wire) Run(ctx context.Context, command string) (string, error) {
	result, err := w.Stream(ctx, command, nil)
	if err != nil {
		return "", err
	}
	if result.Code != 0 {
		return "", errors.New(result.Stderr)
	}
	return result.Stdout, nil
}

func (w wire) Stream(_ context.Context, command string, stdin io.Reader) (session.Result, error) {
	b := w.b
	fed, err := drained(stdin)
	if err != nil {
		return session.Result{}, err
	}
	b.mu.Lock()
	b.ran = append(b.ran, command)
	b.fed = append(b.fed, fed)
	answer, broke, hook := b.answer, b.broke, b.after
	b.mu.Unlock()

	if broke != nil {
		if err := broke(command); err != nil {
			return session.Result{}, err
		}
	}
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
	case command == "uname -m":
		return session.Result{Stdout: b.facts.Arch + "\n"}
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

func settledOn(t *testing.T, class providerkit.Class) *bench {
	t.Helper()

	keys := []byte(aKey + "\n")
	items := Items(class, keys, ArchAMD64)
	minted := []byte("the key this box minted for itself")
	standing := make([]Item, 0, len(items)+1)
	for _, item := range items {
		if item.Kind == KindSealKey {
			item.Content = minted
		}
		standing = append(standing, item)
	}
	stamp, err := Stamp{
		Schema:  providerkit.BootstrapSchema,
		State:   StateComplete,
		Writer:  "the-suite",
		Seal:    Seal{Fingerprint: contentSum(minted), Algorithm: SealAlgorithm, CreatedAt: "2026-01-01T00:00:00Z"},
		Digests: digests(items),
	}.item(class)
	if err != nil {
		t.Fatal(err)
	}
	stood := machine(map[providerkit.Class][]Item{class: append(standing, stamp)})
	seeded := string(proxyBaseline)
	proxied := servesProxy(stood, &seeded)
	stood.answer = func(command string) (session.Result, bool) {
		if command == "cat ~/.ssh/authorized_keys 2>/dev/null" {
			return session.Result{Stdout: aKey + "\n"}, true
		}
		return proxied(command)
	}
	return stood
}

func TestABoxStandingAtTheStampAWriteLeftDescribesItselfAsCurrent(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	stood := settledOn(t, class)

	described, err := Bootstrap(stood.host()).Describe(context.Background(), class)
	if err != nil {
		t.Fatalf("Describe() = %v", err)
	}
	if !described.Present {
		t.Fatal("Describe() reads no bootstrap where a stamp stands")
	}
	if !described.Stacks[0].DigestCurrent {
		t.Fatalf("Describe() calls a box standing at every digest its stamp records drifted, and a box that can never report itself settled is one every re-run rewrites: %+v",
			described.Stacks)
	}
}

func TestOneItemTheReadingCannotHashIsDriftRatherThanAnAbsence(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	stood := settledOn(t, class)
	held := stood.stands[class]
	for at, item := range held {
		if item.Name == ProxyHelper {
			held[at].Content = nil
		}
	}

	read, err := stood.host().Read(context.Background(), class)
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	if read.current(proxyItem(KindFile)) {
		t.Fatal("the reading calls a helper it read no bytes of current, so this test proves nothing about what drift is")
	}
	if !read.standing(KindFile, ProxyHelper) {
		t.Errorf("%s is absent from a reading every write is planned against, and absence is a lie there: a file this reading could not hash is drift it must name",
			ProxyHelper)
	}
	if read.settled() {
		t.Errorf("a box whose %s does not hash as ocel writes it reports itself settled", ProxyHelper)
	}
}

func digested(document string) string {
	sum := sha256.Sum256([]byte(document))
	return hex.EncodeToString(sum[:])
}

func readsProxy(command string) bool {
	return strings.Contains(command, "sha256sum "+quoted(ProxyConfig)+" | cut")
}

func writesProxy(command string) bool {
	return strings.Contains(command, `cat > "$staged"`)
}

func expectedDigest(command string) string {
	_, checked, _ := strings.Cut(command, `if [ "$held" != `)
	expected, _, _ := strings.Cut(checked, " ]")
	return strings.Trim(expected, "'")
}

func servesProxy(b *bench, held *string) func(string) (session.Result, bool) {
	return func(command string) (session.Result, bool) {
		b.mu.Lock()
		defer b.mu.Unlock()
		switch {
		case writesProxy(command):
			if expected := expectedDigest(command); expected != digested(*held) {
				return session.Result{Code: proxyMoved, Stderr: digested(*held)}, true
			}
			*held = b.fed[len(b.fed)-1]
			return session.Result{Stdout: digested(*held)}, true
		case readsProxy(command):
			return session.Result{Stdout: digested(*held) + "\n" + *held}, true
		default:
			return session.Result{}, false
		}
	}
}
