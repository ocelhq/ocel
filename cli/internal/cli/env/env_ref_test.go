package env

import (
	"bytes"
	"context"
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func ownedElsewhere(t *testing.T, key, value string) {
	t.Helper()
	store, err := clitest.LoadFakeStore()
	if err != nil {
		t.Fatalf("load the fake store: %v", err)
	}
	c := &envvarsv1.Coordinate{Slug: "platform", Key: key}
	if err := store.Write(environmentv1.Tier_TIER_PRODUCTION, c, clitest.FakeCellData{Value: value}); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}

func envRef(t *testing.T, root, key string, opts envOptions, ref envRefOptions) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runEnvRef(context.Background(), clitest.NewDeps(), root, key, opts, ref, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvRef(%s) err = %v; stdout=%s stderr=%s", key, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func envGet(t *testing.T, root, key string, opts envOptions) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runEnvGet(context.Background(), clitest.NewDeps(), root, key, opts, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvGet(%s) err = %v; stdout=%s stderr=%s", key, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func TestRunEnvRef(t *testing.T) {
	t.Run("reads a value another project owns and keeps reading it", func(t *testing.T) {
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
	})

	t.Run("refuses to point at another reference", func(t *testing.T) {
		root := setUpEnvFixture(t)
		ownedElsewhere(t, "STRIPE_API_KEY", "sk_live_secret")
		envRef(t, root, "STRIPE_API_KEY", envOptions{}, envRefOptions{project: "platform"})

		var stdout, stderr bytes.Buffer
		err := runEnvRef(context.Background(), clitest.NewDeps(), root, "POSTHOG_ID", envOptions{}, envRefOptions{key: "STRIPE_API_KEY"}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvRef at a reference err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "reference") {
			t.Errorf("refusal = %q, want it to say a reference may only point at a value", err)
		}
	})

	t.Run("points at a value not set yet", func(t *testing.T) {
		root := setUpEnvFixture(t)

		var stdout, stderr bytes.Buffer
		if err := runEnvRef(context.Background(), clitest.NewDeps(), root, "STRIPE_API_KEY", envOptions{}, envRefOptions{project: "platform"}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvRef at a cell not set yet err = %v; stderr=%s", err, stderr.String())
		}

		var out, errs bytes.Buffer
		err := runEnvGet(context.Background(), clitest.NewDeps(), root, "STRIPE_API_KEY", envOptions{reveal: true}, &out, &errs)
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
	})
}

func TestRunEnvRefs(t *testing.T) {
	t.Run("lists what reads a value, and says so plainly when nothing does", func(t *testing.T) {
		root := setUpEnvFixture(t)
		envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{})

		store, err := clitest.LoadFakeStore()
		if err != nil {
			t.Fatalf("load the fake store: %v", err)
		}
		target := &envvarsv1.Coordinate{Slug: "test-app", Key: "STRIPE_API_KEY"}
		consumer := &envvarsv1.Coordinate{Slug: "billing", Folder: "/api", Key: "STRIPE_API_KEY"}
		pointer := clitest.CoordinateOf(target)
		if err := store.Write(environmentv1.Tier_TIER_PRODUCTION, consumer, clitest.FakeCellData{Target: &pointer}); err != nil {
			t.Fatalf("seed the consumer: %v", err)
		}

		var stdout, stderr bytes.Buffer
		if err := runEnvRefs(context.Background(), clitest.NewDeps(), root, "STRIPE_API_KEY", envOptions{}, &stdout, &stderr); err != nil {
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
		if err := runEnvRefs(context.Background(), clitest.NewDeps(), root, "POSTHOG_ID", envOptions{}, &none, &stderr); err != nil {
			t.Fatalf("runEnvRefs err = %v", err)
		}
		if !strings.Contains(none.String(), "Nothing references") {
			t.Errorf("refs stdout for an unreferenced value = %q, want it to say so plainly", none.String())
		}
	})
}

func TestEnvReferences(t *testing.T) {
	t.Run("get says a cell is a reference and where its value is edited", func(t *testing.T) {
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
	})

	t.Run("set refuses to edit through a reference", func(t *testing.T) {
		root := setUpEnvFixture(t)
		ownedElsewhere(t, "STRIPE_API_KEY", "sk_live_secret")
		envRef(t, root, "STRIPE_API_KEY", envOptions{}, envRefOptions{project: "platform"})

		var stdout, stderr bytes.Buffer
		err := runEnvSet(context.Background(), clitest.NewDeps(), root, "STRIPE_API_KEY", "an edit in the wrong place", envOptions{}, &stdout, &stderr)
		if err == nil {
			t.Fatal("runEnvSet through a reference err = nil, want a refusal")
		}
		if !strings.Contains(err.Error(), "platform/STRIPE_API_KEY") {
			t.Errorf("refusal = %q, want it to name where the value is edited", err)
		}
		if got := strings.TrimSpace(envGet(t, root, "STRIPE_API_KEY", envOptions{reveal: true})); got != "sk_live_secret" {
			t.Errorf("value after the refused edit = %q, want it untouched", got)
		}
	})

	t.Run("rm removes a reference without touching its source", func(t *testing.T) {
		root := setUpEnvFixture(t)
		ownedElsewhere(t, "STRIPE_API_KEY", "sk_live_secret")
		envRef(t, root, "STRIPE_API_KEY", envOptions{}, envRefOptions{project: "platform"})

		var stdout, stderr bytes.Buffer
		if err := runEnvRm(context.Background(), clitest.NewDeps(), root, "STRIPE_API_KEY", envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvRm err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Removed") {
			t.Errorf("rm stdout = %q, want the reference removed in one step", stdout.String())
		}

		store, err := clitest.LoadFakeStore()
		if err != nil {
			t.Fatalf("load the fake store: %v", err)
		}
		source := store[clitest.FakeCoordinateID(environmentv1.Tier_TIER_PRODUCTION, &envvarsv1.Coordinate{Slug: "platform", Key: "STRIPE_API_KEY"})]
		if source.LiveVersion() == 0 {
			t.Fatal("the source value went with the reference, want removing a consumer to leave it alone")
		}
		if got, err := store.Resolve(environmentv1.Tier_TIER_PRODUCTION, source); err != nil || got != "sk_live_secret" {
			t.Errorf("source value = %q (err %v), want it untouched", got, err)
		}
	})

	t.Run("ls shows where a reference reads from", func(t *testing.T) {
		root := setUpEnvFixture(t)
		ownedElsewhere(t, "STRIPE_API_KEY", "sk_live_secret")
		envRef(t, root, "STRIPE_API_KEY", envOptions{}, envRefOptions{project: "platform"})

		var stdout, stderr bytes.Buffer
		if err := runEnvLs(context.Background(), clitest.NewDeps(), root, envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvLs err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "platform/STRIPE_API_KEY") {
			t.Errorf("ls stdout = %q, want a reference to show what it reads", out)
		}
		if strings.Contains(out, "sk_live_secret") {
			t.Errorf("ls stdout = %q, want no value printed", out)
		}
	})
}
