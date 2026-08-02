package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	envv1 "github.com/ocelhq/ocel/pkg/proto/env/v1"
)

// ownedElsewhere is a value belonging to a project this one is not: the
// arrangement a reference exists for, and one no command of this project's can
// create, because every command addresses its own slug.
func ownedElsewhere(t *testing.T, key, value string) {
	t.Helper()
	store, err := loadFakeStore()
	if err != nil {
		t.Fatalf("load the fake store: %v", err)
	}
	c := &envv1.Coordinate{Slug: "platform", Key: key}
	if err := store.write(deploymentsv1.Environment_CLASS_PRODUCTION, c, fakeCellData{Value: value}); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}

func envRef(t *testing.T, root, key string, opts envOptions, ref envRefOptions) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runEnvRef(context.Background(), root, key, opts, ref, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvRef(%s) err = %v; stdout=%s stderr=%s", key, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func envGet(t *testing.T, root, key string, opts envOptions) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runEnvGet(context.Background(), root, key, opts, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvGet(%s) err = %v; stdout=%s stderr=%s", key, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// The credential is set once, in the project that owns it, and read from
// another. Nothing is copied, so an edit at the source is what the consumer
// reads on its very next read — with no re-pointing, no re-run and nothing in
// between that could be forgotten.
func TestRunEnvRef_ReadsAValueAnotherProjectOwnsAndKeepsReadingIt(t *testing.T) {
	root := setUpEnvFixture(t)
	ownedElsewhere(t, "STRIPE_API_KEY", "sk_live_first")

	out := envRef(t, root, "STRIPE_API_KEY", envOptions{}, envRefOptions{project: "platform"})
	if !strings.Contains(out, "platform/STRIPE_API_KEY") {
		t.Errorf("ref stdout = %q, want it to name the cell the value is read from", out)
	}

	if got := strings.TrimSpace(envGet(t, root, "STRIPE_API_KEY", envOptions{reveal: true})); got != "sk_live_first" {
		t.Errorf("revealed value = %q, want the owner's", got)
	}

	ownedElsewhere(t, "STRIPE_API_KEY", "sk_live_rotated")
	if got := strings.TrimSpace(envGet(t, root, "STRIPE_API_KEY", envOptions{reveal: true})); got != "sk_live_rotated" {
		t.Errorf("revealed value after an edit at the source = %q, want %q with nothing re-run here", got, "sk_live_rotated")
	}
}

// A reference is an address, and an address is what `get` shows without
// --reveal: the cell it reads, and where the value behind it is edited.
func TestRunEnvGet_SaysACellIsAReferenceAndWhereItsValueIsEdited(t *testing.T) {
	root := setUpEnvFixture(t)
	ownedElsewhere(t, "STRIPE_API_KEY", "sk_live_secret")
	envRef(t, root, "STRIPE_API_KEY", envOptions{}, envRefOptions{project: "platform"})

	out := envGet(t, root, "STRIPE_API_KEY", envOptions{})
	if !strings.Contains(out, "references platform/STRIPE_API_KEY") {
		t.Errorf("get stdout = %q, want it to say what the cell references", out)
	}
	if strings.Contains(out, "sk_live_secret") {
		t.Errorf("get stdout = %q, want the value withheld without --reveal, reference or not", out)
	}
}

// Editing happens at the source. A write to a reference is refused rather than
// filed as a value of its own, which would leave two cells claiming to be the
// same one and no way to tell which an application read.
func TestRunEnvSet_RefusesToEditThroughAReference(t *testing.T) {
	root := setUpEnvFixture(t)
	ownedElsewhere(t, "STRIPE_API_KEY", "sk_live_secret")
	envRef(t, root, "STRIPE_API_KEY", envOptions{}, envRefOptions{project: "platform"})

	var stdout, stderr bytes.Buffer
	err := runEnvSet(context.Background(), root, "STRIPE_API_KEY", "an edit in the wrong place", envOptions{}, &stdout, &stderr)
	if err == nil {
		t.Fatal("runEnvSet through a reference err = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "platform/STRIPE_API_KEY") {
		t.Errorf("refusal = %q, want it to name where the value is edited", err)
	}
	if got := strings.TrimSpace(envGet(t, root, "STRIPE_API_KEY", envOptions{reveal: true})); got != "sk_live_secret" {
		t.Errorf("value after the refused edit = %q, want it untouched", got)
	}
}

// A chain of two would have to be followed twice and could be closed into a
// loop, so it is refused at the moment it would be created rather than guarded
// against on every read.
func TestRunEnvRef_RefusesToPointAtAnotherReference(t *testing.T) {
	root := setUpEnvFixture(t)
	ownedElsewhere(t, "STRIPE_API_KEY", "sk_live_secret")
	envRef(t, root, "STRIPE_API_KEY", envOptions{}, envRefOptions{project: "platform"})

	var stdout, stderr bytes.Buffer
	err := runEnvRef(context.Background(), root, "POSTHOG_ID", envOptions{}, envRefOptions{key: "STRIPE_API_KEY"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("runEnvRef at a reference err = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "reference") {
		t.Errorf("refusal = %q, want it to say a reference may only point at a value", err)
	}
}

// The value and the reference to it are as often two people's work as one's,
// so which lands first is not something the CLI has an opinion about. Until the
// value is there, reading through the reference names what is missing.
func TestRunEnvRef_PointsAtAValueNotSetYet(t *testing.T) {
	root := setUpEnvFixture(t)

	var stdout, stderr bytes.Buffer
	if err := runEnvRef(context.Background(), root, "STRIPE_API_KEY", envOptions{}, envRefOptions{project: "platform"}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvRef at a cell not set yet err = %v; stderr=%s", err, stderr.String())
	}

	var out, errs bytes.Buffer
	err := runEnvGet(context.Background(), root, "STRIPE_API_KEY", envOptions{reveal: true}, &out, &errs)
	if err == nil {
		t.Fatal("runEnvGet through a reference to nothing err = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "platform/STRIPE_API_KEY") {
		t.Errorf("failure = %q, want it to name the cell that holds nothing", err)
	}

	ownedElsewhere(t, "STRIPE_API_KEY", "sk_live_secret")
	if got := strings.TrimSpace(envGet(t, root, "STRIPE_API_KEY", envOptions{reveal: true})); got != "sk_live_secret" {
		t.Errorf("value once the source was set = %q, want it read through with no second write", got)
	}
}

// Before changing a shared value, an operator has to be able to see what it
// would change. The answer crosses projects, because that is where a consumer
// usually is.
func TestRunEnvRefs_ListsWhatReadsAValue(t *testing.T) {
	root := setUpEnvFixture(t)
	envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{})

	store, err := loadFakeStore()
	if err != nil {
		t.Fatalf("load the fake store: %v", err)
	}
	target := &envv1.Coordinate{Slug: "test-app", Key: "STRIPE_API_KEY"}
	consumer := &envv1.Coordinate{Slug: "billing", Folder: "/api", Key: "STRIPE_API_KEY"}
	pointer := coordinateOf(target)
	if err := store.write(deploymentsv1.Environment_CLASS_PRODUCTION, consumer, fakeCellData{Target: &pointer}); err != nil {
		t.Fatalf("seed the consumer: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := runEnvRefs(context.Background(), root, "STRIPE_API_KEY", envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvRefs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"billing", "/api", "STRIPE_API_KEY"} {
		if !strings.Contains(out, want) {
			t.Errorf("refs stdout = %q, want it to show %q", out, want)
		}
	}
	if strings.Contains(out, "sk_live_secret") {
		t.Errorf("refs stdout = %q, want no value printed", out)
	}

	var none bytes.Buffer
	if err := runEnvRefs(context.Background(), root, "POSTHOG_ID", envOptions{}, &none, &stderr); err != nil {
		t.Fatalf("runEnvRefs err = %v", err)
	}
	if !strings.Contains(none.String(), "Nothing references") {
		t.Errorf("refs stdout for an unreferenced value = %q, want it to say so plainly", none.String())
	}
}

// Removing a reference is removing an item: there is no unlink step, because
// the source never recorded who read it. What is left behind is the value,
// untouched.
func TestRunEnvRm_RemovesAReferenceWithoutTouchingItsSource(t *testing.T) {
	root := setUpEnvFixture(t)
	ownedElsewhere(t, "STRIPE_API_KEY", "sk_live_secret")
	envRef(t, root, "STRIPE_API_KEY", envOptions{}, envRefOptions{project: "platform"})

	var stdout, stderr bytes.Buffer
	if err := runEnvRm(context.Background(), root, "STRIPE_API_KEY", envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvRm err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Removed") {
		t.Errorf("rm stdout = %q, want the reference removed in one step", stdout.String())
	}

	store, err := loadFakeStore()
	if err != nil {
		t.Fatalf("load the fake store: %v", err)
	}
	source := store[fakeCoordinateID(deploymentsv1.Environment_CLASS_PRODUCTION, &envv1.Coordinate{Slug: "platform", Key: "STRIPE_API_KEY"})]
	if source.liveVersion() == 0 {
		t.Fatal("the source value went with the reference, want removing a consumer to leave it alone")
	}
	if got, err := store.resolve(deploymentsv1.Environment_CLASS_PRODUCTION, source); err != nil || got != "sk_live_secret" {
		t.Errorf("source value = %q (err %v), want it untouched", got, err)
	}
}

func TestRunEnvLs_ShowsWhereAReferenceReadsFrom(t *testing.T) {
	root := setUpEnvFixture(t)
	ownedElsewhere(t, "STRIPE_API_KEY", "sk_live_secret")
	envRef(t, root, "STRIPE_API_KEY", envOptions{}, envRefOptions{project: "platform"})

	var stdout, stderr bytes.Buffer
	if err := runEnvLs(context.Background(), root, envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "platform/STRIPE_API_KEY") {
		t.Errorf("ls stdout = %q, want a reference to show what it reads", out)
	}
	if strings.Contains(out, "sk_live_secret") {
		t.Errorf("ls stdout = %q, want no value printed", out)
	}
}
