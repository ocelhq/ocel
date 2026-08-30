package conformance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/ledger"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

func runPorts(t *testing.T, suite Suite) {
	t.Helper()

	if suite.New == nil {
		t.Skip("the suite carries no constructor, so there are no ports to exercise")
	}
	provider, err := suite.New(context.Background(), suite.Options)
	if err != nil {
		t.Fatalf("New() error = %v, want a provider", err)
	}
	RunPorts(t, provider)
}

func RunPorts(t *testing.T, provider providerkit.Provider) {
	t.Helper()

	t.Run("RecordStore", func(t *testing.T) { RunRecordStore(t, provider.Records()) })
	t.Run("Sealer", func(t *testing.T) { RunSealer(t, provider.Sealer()) })
	t.Run("Bootstrapper", func(t *testing.T) { RunBootstrapper(t, bootstrapperOf(t, provider)) })
	t.Run("ArtifactStore", func(t *testing.T) { RunArtifactStore(t, provider.Artifacts()) })
	t.Run("Releaser", func(t *testing.T) { RunReleaser(t, provider.Releases(), provider.Artifacts(), provider.Serves()) })
	t.Run("Credentials", func(t *testing.T) { RunCredentials(t, provider.Credentials()) })
	t.Run("EdgeRegistry", func(t *testing.T) { RunEdgeRegistry(t, provider.Edges()) })
	t.Run("DNSRegistry", func(t *testing.T) { RunDNSRegistry(t, provider.DNS()) })
}

func bootstrapperOf(t *testing.T, provider providerkit.Provider) providerkit.Bootstrapper {
	t.Helper()
	bootstrapper, err := provider.Bootstrap(provider.Edges().Default())
	if err != nil {
		t.Fatalf("Bootstrap(%q) error = %v, want the bootstrapper for this provider's default edge", provider.Edges().Default(), err)
	}
	return bootstrapper
}

func under(t *testing.T, rest ...string) providerkit.RecordName {
	return in(providerkit.ClassProduction, t, rest...)
}

func in(class providerkit.Class, t *testing.T, rest ...string) providerkit.RecordName {
	return append(providerkit.RecordName{providerkit.RootConformance, string(class), t.Name()}, rest...)
}

func RunRecordStore(t *testing.T, records providerkit.RecordStore) {
	t.Helper()

	ctx := context.Background()

	t.Run("an unwritten name is no record", func(t *testing.T) {
		if _, err := records.Read(ctx, under(t, "never-written")); !errors.Is(err, providerkit.ErrNoRecord) {
			t.Fatalf("Read() of a name never written = %v, want ErrNoRecord", err)
		}
	})

	t.Run("a first write claims the name", func(t *testing.T) {
		name := under(t, "claimed")
		revision, err := records.Write(ctx, providerkit.Record{Name: name, Bytes: []byte("one")})
		if err != nil {
			t.Fatalf("Write() of a new record = %v, want it stored", err)
		}
		if revision == "" {
			t.Fatal("Write() returned an empty revision, and a compare-and-set has nothing to compare")
		}
		held, err := records.Read(ctx, name)
		if err != nil || !bytes.Equal(held.Bytes, []byte("one")) {
			t.Fatalf("Read() = %q, %v, want the bytes just written", held.Bytes, err)
		}
		if held.Revision != revision {
			t.Fatalf("Read() revision = %q, want the %q Write() reported", held.Revision, revision)
		}
	})

	t.Run("a second write at the same name must name a revision", func(t *testing.T) {
		name := under(t, "occupied")
		if _, err := records.Write(ctx, providerkit.Record{Name: name, Bytes: []byte("one")}); err != nil {
			t.Fatal(err)
		}
		if _, err := records.Write(ctx, providerkit.Record{Name: name, Bytes: []byte("two")}); !errors.Is(err, providerkit.ErrStale) {
			t.Fatalf("Write() at a taken name with no revision = %v, want ErrStale", err)
		}
	})

	t.Run("a write at the revision read wins and a later one loses", func(t *testing.T) {
		name := under(t, "compared")
		if _, err := records.Write(ctx, providerkit.Record{Name: name, Bytes: []byte("one")}); err != nil {
			t.Fatal(err)
		}
		held, err := records.Read(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		held.Bytes = []byte("two")
		if _, err := records.Write(ctx, held); err != nil {
			t.Fatalf("Write() at the revision read = %v, want it stored", err)
		}
		held.Bytes = []byte("three")
		if _, err := records.Write(ctx, held); !errors.Is(err, providerkit.ErrStale) {
			t.Fatalf("a second write at a revision that moved = %v, want ErrStale", err)
		}
	})

	t.Run("a pair lands whole or not at all", func(t *testing.T) {
		record, value := under(t, "pair", "record"), under(t, "pair", "value")
		if err := records.WritePair(ctx,
			providerkit.Record{Name: record, Bytes: []byte("one")},
			providerkit.Record{Name: value, Bytes: []byte("one")},
		); err != nil {
			t.Fatalf("WritePair() of two new records = %v, want both stored", err)
		}
		held, err := records.Read(ctx, record)
		if err != nil {
			t.Fatal(err)
		}
		beside, err := records.Read(ctx, value)
		if err != nil {
			t.Fatal(err)
		}

		moved := beside
		moved.Revision = "a revision nobody wrote"
		held.Bytes, moved.Bytes = []byte("two"), []byte("two")
		if err := records.WritePair(ctx, held, moved); !errors.Is(err, providerkit.ErrStale) {
			t.Fatalf("WritePair() where one half moved = %v, want ErrStale", err)
		}
		for _, name := range []providerkit.RecordName{record, value} {
			stood, err := records.Read(ctx, name)
			if err != nil || !bytes.Equal(stood.Bytes, []byte("one")) {
				t.Fatalf("Read(%s) after a refused pair write = %q, %v, want the bytes from the write that landed", name, stood.Bytes, err)
			}
		}
	})

	t.Run("a removal names the revision it read", func(t *testing.T) {
		name := under(t, "removed")
		revision, err := records.Write(ctx, providerkit.Record{Name: name, Bytes: []byte("one")})
		if err != nil {
			t.Fatal(err)
		}
		if err := records.Remove(ctx, name, "a revision nobody wrote"); !errors.Is(err, providerkit.ErrStale) {
			t.Fatalf("Remove() at a revision that never held = %v, want ErrStale", err)
		}
		if err := records.Remove(ctx, name, revision); err != nil {
			t.Fatalf("Remove() at the revision held = %v, want it gone", err)
		}
		if _, err := records.Read(ctx, name); !errors.Is(err, providerkit.ErrNoRecord) {
			t.Fatalf("Read() after Remove() = %v, want ErrNoRecord", err)
		}
	})

	t.Run("one class's records are not the other's", func(t *testing.T) {
		production, preview := in(providerkit.ClassProduction, t, "held"), in(providerkit.ClassPreview, t, "held")
		if _, err := records.Write(ctx, providerkit.Record{Name: production, Bytes: []byte("production")}); err != nil {
			t.Fatal(err)
		}
		if _, err := records.Read(ctx, preview); !errors.Is(err, providerkit.ErrNoRecord) {
			t.Fatalf("Read() of the preview name after only production was written = %v, want ErrNoRecord", err)
		}
		if _, err := records.Write(ctx, providerkit.Record{Name: preview, Bytes: []byte("preview")}); err != nil {
			t.Fatal(err)
		}
		held, err := records.Read(ctx, production)
		if err != nil || string(held.Bytes) != "production" {
			t.Fatalf("Read() of the production name = %q, %v, want the production bytes untouched", held.Bytes, err)
		}
	})

	t.Run("List answers with everything under a prefix", func(t *testing.T) {
		leaves := []providerkit.RecordName{
			under(t, "tree", "a"),
			under(t, "tree", "b", "one"),
			under(t, "tree", "b", "two"),
		}
		for _, name := range leaves {
			if _, err := records.Write(ctx, providerkit.Record{Name: name, Bytes: []byte(name.String())}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := records.Write(ctx, providerkit.Record{Name: under(t, "treeish"), Bytes: []byte("not under it")}); err != nil {
			t.Fatal(err)
		}

		held, err := records.List(ctx, under(t, "tree"))
		if err != nil {
			t.Fatalf("List() = %v", err)
		}
		if len(held) != len(leaves) {
			t.Fatalf("List() returned %d records, want the %d written under the prefix and nothing beside them", len(held), len(leaves))
		}
		for _, record := range held {
			if !bytes.Equal(record.Bytes, []byte(record.Name.String())) {
				t.Errorf("List() returned %s carrying %q, want the bytes written at that name", record.Name, record.Bytes)
			}
			if record.Revision == "" {
				t.Errorf("List() returned %s with no revision, and a caller cannot then remove it", record.Name)
			}
		}

		deeper, err := records.List(ctx, under(t, "tree", "b"))
		if err != nil || len(deeper) != 2 {
			t.Fatalf("List() of a deeper prefix returned %d records, %v, want 2", len(deeper), err)
		}

		whole, err := records.List(ctx, under(t))
		if err != nil || len(whole) != len(leaves)+1 {
			t.Fatalf("List() at the name this suite roots itself at returned %d records, %v, want the %d written beneath it", len(whole), err, len(leaves)+1)
		}
	})

	t.Run("every prefix the kit reads a whole subtree at is one this store can answer", func(t *testing.T) {
		scope := values.Scope{Project: "conformance", Class: providerkit.ClassProduction}
		for _, name := range []providerkit.RecordName{
			providerkit.ProjectsRecord(providerkit.ClassProduction),
			providerkit.StacksRecord(providerkit.ClassProduction, scope.Project),
			providerkit.EdgeStacksRecord(providerkit.ClassProduction),
			providerkit.LedgerRecord(ledger.Scope(providerkit.ClassProduction, scope.Project)),
			values.Under(scope),
			values.Refs(scope),
		} {
			if _, err := records.List(ctx, name); err != nil {
				t.Errorf("List(%s) = %v, want a store that partitions no deeper than the kit reads", name, err)
			}
		}
	})
}

func RunSealer(t *testing.T, sealer providerkit.Sealer) {
	t.Helper()

	ctx := context.Background()

	at := providerkit.Coordinate{
		Project: "shop",
		Class:   providerkit.ClassProduction,
		Env:     "*",
		Folder:  "/",
		Name:    "DATABASE_URL",
	}
	plaintext := []byte("postgres://example")

	sealed, err := sealer.Seal(ctx, at, plaintext)
	if err != nil {
		t.Fatalf("Seal() = %v", err)
	}
	if bytes.Contains(sealed, plaintext) {
		t.Fatal("Seal() returned the plaintext inside its output")
	}

	opened, err := sealer.Open(ctx, at, sealed)
	if err != nil {
		t.Fatalf("Open() at the coordinate sealed = %v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("Open() = %q, want %q", opened, plaintext)
	}

	for name, moved := range map[string]providerkit.Coordinate{
		"another project":     {Project: "other", Class: at.Class, Env: at.Env, Folder: at.Folder, Name: at.Name},
		"another class":       {Project: at.Project, Class: providerkit.ClassPreview, Env: at.Env, Folder: at.Folder, Name: at.Name},
		"another environment": {Project: at.Project, Class: at.Class, Env: "staging", Folder: at.Folder, Name: at.Name},
		"another folder":      {Project: at.Project, Class: at.Class, Env: at.Env, Folder: "/apps/web", Name: at.Name},
		"another key":         {Project: at.Project, Class: at.Class, Env: at.Env, Folder: at.Folder, Name: "API_KEY"},
	} {
		t.Run("a value sealed here does not open at "+name, func(t *testing.T) {
			if _, err := sealer.Open(ctx, moved, sealed); err == nil {
				t.Fatal("Open() at a coordinate the value was not sealed at succeeded, so the coordinate is not authenticated")
			}
		})
	}
}

func RunBootstrapper(t *testing.T, bootstrapper providerkit.Bootstrapper) {
	t.Helper()

	ctx := context.Background()
	catalogue := bootstrapper.Catalogue()

	t.Run("every feature is one the catalogue can stand up", func(t *testing.T) {
		named := make([]string, 0, len(catalogue))
		for _, f := range catalogue {
			if f.Name == "" {
				t.Error("the catalogue carries a feature with no name, and nothing can ask for it")
			}
			named = append(named, f.Name)
		}
		for _, f := range catalogue {
			for _, dep := range f.DependsOn {
				if !slices.Contains(named, dep) {
					t.Errorf("%s depends on %q, which this provider does not offer", f.Name, dep)
				}
			}
			for _, need := range f.Needs {
				if !strings.HasPrefix(need, providerkit.NeedsFrameworkPrefix) && !strings.HasPrefix(need, providerkit.NeedsEdgePrefix) {
					t.Errorf("%s needs %q, which is neither a %s nor an %s token", f.Name, need, providerkit.NeedsFrameworkPrefix, providerkit.NeedsEdgePrefix)
				}
			}
		}
		if _, err := providerkit.FeatureLevels(catalogue, named); err != nil {
			t.Fatalf("FeatureLevels() over the whole catalogue = %v, want an order that stands every feature up", err)
		}
	})

	t.Run("Describe answers for the class it was asked about", func(t *testing.T) {
		for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
			described, err := bootstrapper.Describe(ctx, class)
			if err != nil {
				t.Fatalf("Describe(%s) = %v", class, err)
			}
			if described.Class != class {
				t.Errorf("Describe(%s) answered for %s", class, described.Class)
			}
			for _, stack := range described.Stacks {
				if stack.Name == "" {
					t.Errorf("Describe(%s) returned a stack with no name, and no plan can name it", class)
				}
			}
		}
	})

	t.Run("Plan answers for the request Apply would be given", func(t *testing.T) {
		class := providerkit.ClassProduction
		described, err := bootstrapper.Describe(ctx, class)
		if err != nil {
			t.Fatalf("Describe(%s) = %v", class, err)
		}
		plan, err := bootstrapper.Plan(ctx, providerkit.BootstrapRequest{Class: class})
		if err != nil {
			t.Fatalf("Plan(%s) = %v", class, err)
		}
		creates := 0
		for _, group := range plan.Groups {
			if group.Name == "" {
				t.Errorf("Plan() returned %+v, and no plan can render a nameless group", group)
			}
			if !providerkit.ValidChangeAction(group.Action) {
				t.Errorf("Plan() returned group action %q, which is none the plan knows", group.Action)
			}
			if group.Action == providerkit.ActionUpdate && len(group.Changes) == 0 && group.Reason == "" {
				t.Errorf("Plan() returned %q as an update with neither children nor a reason, which reads as no change at all", group.Name)
			}
			if group.Action == providerkit.ActionCreate {
				creates++
			}
			for _, change := range group.Changes {
				if change.Name == "" {
					t.Errorf("Plan() returned %+v under %q, and no plan can render a nameless change", change, group.Name)
				}
				if !providerkit.ValidChangeAction(change.Action) {
					t.Errorf("Plan() returned change action %q, which is none the plan knows", change.Action)
				}
			}
		}
		if !described.Present && creates == 0 {
			t.Error("Plan() against an account with no bootstrap creates nothing, and an apply from here would create the whole bootstrap")
		}
	})

	t.Run("Plan shows a dropped feature leaving", func(t *testing.T) {
		if len(catalogue) == 0 {
			t.Skip("this provider offers no features, so nothing can be dropped")
		}
		class := providerkit.ClassProduction
		drop := featureNames(catalogue)
		plan, err := bootstrapper.Plan(ctx, providerkit.BootstrapRequest{Class: class, Remove: drop})
		if err != nil {
			t.Fatalf("Plan(%s, drop %v) = %v", class, drop, err)
		}
		leaving := map[string]providerkit.ChangeAction{}
		for _, group := range plan.Groups {
			if group.Feature != "" {
				leaving[group.Feature] = group.Action
			}
		}
		for _, name := range drop {
			action, planned := leaving[name]
			if !planned {
				t.Errorf("Plan() drops %q and shows no group for it; the plan is the only thing asked about before the apply", name)
				continue
			}
			if action != providerkit.ActionDelete {
				t.Errorf("Plan() shows %q as %q though it was dropped, and a drop takes its stack down", name, action)
			}
		}
	})

	t.Run("what Apply stands up, Describe reports and Remove takes down", func(t *testing.T) {
		class := providerkit.ClassPreview
		levels, err := providerkit.FeatureLevels(catalogue, featureNames(catalogue))
		if err != nil {
			t.Fatal(err)
		}
		var wanted []string
		for _, level := range levels {
			wanted = append(wanted, level...)
		}
		if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Features: wanted}, nil); err != nil {
			t.Fatalf("Apply() of the whole catalogue = %v", err)
		}

		described, err := bootstrapper.Describe(ctx, class)
		if err != nil {
			t.Fatalf("Describe() after Apply() = %v", err)
		}
		for _, stack := range described.Stacks {
			if stack.Feature != "" && !slices.Contains(wanted, stack.Feature) {
				t.Errorf("Describe() reports a stack for %q, which Apply() was never asked for", stack.Feature)
			}
		}

		removal, err := bootstrapper.PlanRemoval(ctx, class)
		if err != nil {
			t.Fatalf("PlanRemoval() = %v", err)
		}
		for _, group := range removal.Groups {
			if group.Kind == "" || group.Name == "" {
				t.Errorf("PlanRemoval() returned %+v, and a removal plan cannot render a nameless group", group)
			}
			if !providerkit.ValidChangeAction(group.Action) {
				t.Errorf("PlanRemoval() returned action %q, which is none the plan knows", group.Action)
			}
			for _, change := range group.Changes {
				if change.Kind == "" || change.Name == "" {
					t.Errorf("PlanRemoval() returned row %+v, and a removal plan cannot render a nameless row", change)
				}
				if !providerkit.ValidChangeAction(change.Action) {
					t.Errorf("PlanRemoval() returned row action %q, which is none the plan knows", change.Action)
				}
			}
		}

		if err := bootstrapper.Remove(ctx, class, nil); err != nil {
			t.Fatalf("Remove() = %v", err)
		}
		gone, err := bootstrapper.Describe(ctx, class)
		if err != nil {
			t.Fatalf("Describe() after Remove() = %v", err)
		}
		if gone.Present {
			t.Error("Describe() still reports the bootstrap present after Remove()")
		}
	})

	t.Run("Apply takes a drop in delete order", func(t *testing.T) {
		class := providerkit.ClassPreview
		drop, err := providerkit.FeatureLevels(catalogue, featureNames(catalogue))
		if err != nil {
			t.Fatal(err)
		}
		var ordered []string
		for i := len(drop) - 1; i >= 0; i-- {
			ordered = append(ordered, drop[i]...)
		}
		if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Remove: ordered}, nil); err != nil {
			t.Fatalf("Apply() dropping every feature = %v", err)
		}
		if err := bootstrapper.Remove(ctx, class, nil); err != nil {
			t.Fatalf("Remove() = %v", err)
		}
	})
}

func featureNames(catalogue []providerkit.Feature) []string {
	out := make([]string, 0, len(catalogue))
	for _, f := range catalogue {
		out = append(out, f.Name)
	}
	return out
}

func RunCredentials(t *testing.T, credentials providerkit.Credentials) {
	t.Helper()

	ctx := context.Background()

	t.Run("Whoami either says who this is or refuses as denied", func(t *testing.T) {
		identity, err := credentials.Whoami(ctx)
		if err != nil {
			var refusal providerkit.Refusal
			if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeDenied {
				t.Fatalf("Whoami() failed with %v, want a Refusal carrying %s so the CLI can render a credential problem", err, providerkit.CodeDenied)
			}
			if refusal.Message == "" {
				t.Error("Whoami() refused with no message, so the CLI has nothing to tell the user")
			}
			return
		}
		if identity.Provider == "" {
			t.Error("Whoami() answered an identity naming no provider")
		}
		for _, detail := range identity.Details {
			if detail.Label == "" {
				t.Errorf("Whoami() returned a detail with no label: %+v", detail)
			}
		}
	})

	t.Run("permissions are rendered for either tier", func(t *testing.T) {
		for _, tier := range []providerkit.CredentialTier{providerkit.TierBootstrap, providerkit.TierDeploy} {
			if _, err := credentials.Permissions(tier); err != nil {
				t.Errorf("Permissions(%s) = %v, want the permissions that tier needs", tier, err)
			}
		}
	})
}

func RunArtifactStore(t *testing.T, artifacts providerkit.ArtifactStore) {
	t.Helper()

	ctx := context.Background()
	ref := providerkit.ArtifactRef{Class: providerkit.ClassProduction, Bucket: providerkit.StoreFunctions, Key: "conformance/" + t.Name() + "/bundle.zip"}
	body := []byte("a build artifact")

	if _, storeless := artifacts.(providerkit.NoArtifacts); storeless {
		runStorelessArtifactStore(t, artifacts, ref)
		return
	}

	t.Run("what Put stores, Open reads back", func(t *testing.T) {
		if err := artifacts.Put(ctx, ref, bytes.NewReader(body)); err != nil {
			t.Fatalf("Put() = %v, want the artifact stored", err)
		}
		opened, err := artifacts.Open(ctx, ref)
		if err != nil {
			t.Fatalf("Open() of an artifact just put = %v", err)
		}
		defer opened.Close()
		read, err := io.ReadAll(opened)
		if err != nil || !bytes.Equal(read, body) {
			t.Fatalf("Open() read %q, %v, want %q", read, err, body)
		}
	})

	t.Run("Has answers for a key stored and for one nothing wrote", func(t *testing.T) {
		held, err := artifacts.Has(ctx, ref)
		if err != nil || !held {
			t.Errorf("Has() of an artifact just put = %v, %v, want true: a deploy re-uploads every unchanged build without it", held, err)
		}
		absent := providerkit.ArtifactRef{Class: ref.Class, Bucket: ref.Bucket, Key: ref.Key + ".never-written"}
		held, err = artifacts.Has(ctx, absent)
		if err != nil {
			t.Errorf("Has() of a key nothing wrote = %v, want a plain false", err)
		}
		if held {
			t.Error("Has() claims a key nothing wrote is stored, so a changed build would never be uploaded")
		}
	})

	t.Run("Open of a key nothing wrote refuses rather than answering empty", func(t *testing.T) {
		absent := providerkit.ArtifactRef{Class: ref.Class, Bucket: ref.Bucket, Key: ref.Key + ".never-written"}
		opened, err := artifacts.Open(ctx, absent)
		if err == nil {
			opened.Close()
			t.Fatal("Open() of a key nothing wrote succeeded, so a missing artifact reads as an empty one")
		}
	})

	t.Run("RemovePrefix takes the prefix and nothing beside it", func(t *testing.T) {
		kept := providerkit.ArtifactRef{Class: ref.Class, Bucket: ref.Bucket, Key: "conformance/" + t.Name() + "-sibling/bundle.zip"}
		if err := artifacts.Put(ctx, kept, bytes.NewReader(body)); err != nil {
			t.Fatal(err)
		}
		if err := artifacts.RemovePrefix(ctx, ref.Class, "conformance/"+t.Name()+"/", nil); err != nil {
			t.Fatalf("RemovePrefix() = %v", err)
		}
		opened, err := artifacts.Open(ctx, kept)
		if err != nil {
			t.Fatalf("Open() of a key outside the prefix removed = %v, want it still stored", err)
		}
		opened.Close()
	})

	t.Run("RemovePrefix of one class leaves the other class's artifacts", func(t *testing.T) {
		key := "conformance/" + t.Name() + "/bundle.zip"
		production := providerkit.ArtifactRef{Class: providerkit.ClassProduction, Bucket: ref.Bucket, Key: key}
		preview := providerkit.ArtifactRef{Class: providerkit.ClassPreview, Bucket: ref.Bucket, Key: key}
		for _, at := range []providerkit.ArtifactRef{production, preview} {
			if err := artifacts.Put(ctx, at, bytes.NewReader(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := artifacts.RemovePrefix(ctx, providerkit.ClassPreview, "conformance/"+t.Name()+"/", nil); err != nil {
			t.Fatalf("RemovePrefix() = %v", err)
		}
		opened, err := artifacts.Open(ctx, production)
		if err != nil {
			t.Fatalf("Open() of the production artifact after a preview prefix was removed = %v, want it still stored", err)
		}
		opened.Close()
	})

	t.Run("RemovePrefix of a prefix holding nothing is not an error", func(t *testing.T) {
		if err := artifacts.RemovePrefix(ctx, ref.Class, "conformance/"+t.Name()+"/nothing-here/", nil); err != nil {
			t.Fatalf("RemovePrefix() of a prefix nothing was written under = %v, want nil", err)
		}
	})
}

func runStorelessArtifactStore(t *testing.T, artifacts providerkit.ArtifactStore, ref providerkit.ArtifactRef) {
	t.Helper()

	ctx := context.Background()

	t.Run("Put refuses rather than accepting a write nothing will ever read back", func(t *testing.T) {
		var refusal providerkit.Refusal
		err := artifacts.Put(ctx, ref, bytes.NewReader([]byte("a build artifact")))
		if !errors.As(err, &refusal) {
			t.Fatalf("Put() into a provider that keeps no artifacts = %v, want a refusal: a store that reports a write it loses is worse than one that has none", err)
		}
		if refusal.Code != providerkit.CodeInvalid {
			t.Errorf("Put() refused with %q, want %q so the CLI renders it as the caller's mistake", refusal.Code, providerkit.CodeInvalid)
		}
		if refusal.Message == "" {
			t.Error("Put() refused with no message, so the CLI has nothing to tell the user")
		}
	})

	t.Run("Open refuses rather than answering empty", func(t *testing.T) {
		var refusal providerkit.Refusal
		opened, err := artifacts.Open(ctx, ref)
		if !errors.As(err, &refusal) {
			if err == nil {
				opened.Close()
			}
			t.Fatalf("Open() from a provider that keeps no artifacts = %v, want a refusal: a missing artifact must never read as an empty one", err)
		}
	})

	t.Run("Has answers a plain false", func(t *testing.T) {
		held, err := artifacts.Has(ctx, ref)
		if err != nil {
			t.Fatalf("Has() = %v, want a plain false: plan synthesis draws its create row from this answer", err)
		}
		if held {
			t.Error("Has() claims a provider that keeps no artifacts holds one")
		}
	})

	t.Run("RemovePrefix of any prefix, including one nothing wrote under, is nil", func(t *testing.T) {
		for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
			for _, prefix := range []string{"conformance/" + t.Name() + "/", "conformance/" + t.Name() + "/nothing-here/"} {
				if err := artifacts.RemovePrefix(ctx, class, prefix, nil); err != nil {
					t.Errorf("RemovePrefix(%s, %q) = %v, want nil: teardown sweeps it on every destroy and every preview reap", class, prefix, err)
				}
			}
		}
	})
}

func declared(serves []providerkit.LinkType) []providerkit.Resource {
	resources := make([]providerkit.Resource, 0, len(serves))
	for _, kind := range serves {
		resources = append(resources, providerkit.Resource{Name: "c-" + string(kind), Type: kind})
	}
	return resources
}

func planRows(t *testing.T, plan providerkit.Plan, verb string) int {
	t.Helper()

	rows := 0
	for _, group := range plan.Groups {
		if group.Name == "" {
			t.Errorf("%s() returned %+v, and no plan can render a nameless group", verb, group)
		}
		if !providerkit.ValidChangeAction(group.Action) {
			t.Errorf("%s() returned group action %q, which is none the plan knows", verb, group.Action)
		}
		for _, change := range group.Changes {
			if change.Name == "" {
				t.Errorf("%s() returned %+v under %q, and no plan can render a nameless row", verb, change, group.Name)
			}
			if !providerkit.ValidChangeAction(change.Action) {
				t.Errorf("%s() returned change action %q, which is none the plan knows", verb, change.Action)
			}
			rows++
		}
	}
	return rows
}

const conformanceImageDigest = "4f9a2c1e0d3b5a678c9e0f1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e"

type countedImages struct {
	mu     sync.Mutex
	pushed int
}

func (c *countedImages) Has(context.Context, providerkit.ImagePush) (bool, error) { return false, nil }

func (c *countedImages) Push(_ context.Context, _ providerkit.ImagePush, _ providerkit.Reporter) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pushed++
	return nil
}

func (c *countedImages) Pushed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pushed
}

const conformanceArtifactDigest = "b5bb9d8014a0f9b1d61e21e796d78dccdf1352f23cd32812f4850b878ae4944c"

func uploadKey(t *testing.T) string {
	return "conformance/" + naming.Sanitize(t.Name()) + "/" + conformanceArtifactDigest + ".zip"
}

func writtenArtifact(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "artifact.zip")
	if err := os.WriteFile(path, []byte("conformance\n"), 0o644); err != nil {
		t.Fatalf("write the artifact a release would ship: %v", err)
	}
	return path
}

func RunReleaser(t *testing.T, releaser providerkit.Releaser, artifacts providerkit.ArtifactStore, serves []providerkit.LinkType) {
	t.Helper()

	ctx := context.Background()
	ref := providerkit.StackRef{
		Project: "conformance",
		Class:   providerkit.ClassPreview,
		Name:    naming.InfraStack("conformance"),
	}

	t.Run("Destroy of a stack that was never provisioned is a no-op", func(t *testing.T) {
		absent := ref
		absent.Name = naming.InfraStack("never-provisioned")
		if err := releaser.Destroy(ctx, absent, nil); err != nil {
			t.Fatalf("Destroy() of an absent stack = %v, want nil so a rerun of a teardown is safe", err)
		}
	})

	t.Run("Plan says what a provision would do before it does it", func(t *testing.T) {
		resources := declared(serves)
		if len(resources) == 0 {
			t.Skip("this provider serves no resource primitive, so a release asks for nothing")
		}
		planned, err := releaser.Plan(ctx, providerkit.StackPlan{
			Ref:       ref,
			Kind:      providerkit.StackInfra,
			Resources: resources,
		}, nil)
		if err != nil {
			t.Fatalf("Plan() of every primitive this provider serves = %v", err)
		}
		if rows := planRows(t, planned, "Plan"); rows == 0 {
			t.Fatal("Plan() of a release standing up every primitive showed nothing, and the plan is the only thing a human consents to")
		}
	})

	t.Run("an artifact the release must ship is a row the plan shows and an object the apply writes", func(t *testing.T) {
		resources := declared(serves)
		if len(resources) == 0 {
			t.Skip("this provider serves no resource primitive, so a release asks for nothing")
		}
		bare := providerkit.StackPlan{Ref: ref, Kind: providerkit.StackInfra, Resources: resources}
		without, err := releaser.Plan(ctx, bare, nil)
		if err != nil {
			t.Fatalf("Plan() of a release shipping no artifact = %v", err)
		}

		path := writtenArtifact(t)
		shipping := bare
		shipping.Uploads = []providerkit.Upload{{
			Name:   "conformance",
			Ref:    providerkit.ArtifactRef{Class: ref.Class, Bucket: providerkit.StoreFunctions, Key: uploadKey(t)},
			Path:   path,
			Digest: conformanceArtifactDigest,
		}}
		with, err := releaser.Plan(ctx, shipping, nil)
		if err != nil {
			t.Fatalf("Plan() of a release shipping one artifact = %v", err)
		}

		if planRows(t, with, "Plan") <= planRows(t, without, "Plan") {
			t.Error("shipping an artifact added no plan row, and an upload is a mutation of the customer's account like any other: " +
				"the plan and the apply must ship it down one path")
		}

		if _, err := releaser.Provision(ctx, shipping, nil); err != nil {
			t.Fatalf("Provision() of the release whose plan showed the artifact = %v", err)
		}
		shipped := shipping.Uploads[0].Ref
		held, err := artifacts.Has(ctx, shipped)
		if err != nil {
			t.Fatalf("Has() of the artifact the release shipped = %v", err)
		}
		if !held {
			t.Fatal("the plan showed an artifact row and Provision() left nothing in the store, " +
				"so the plan promised a write the apply never made")
		}
		body, err := artifacts.Open(ctx, shipped)
		if err != nil {
			t.Fatalf("Open() of the artifact the release shipped = %v", err)
		}
		defer body.Close()
		got, err := io.ReadAll(body)
		if err != nil {
			t.Fatalf("read the artifact the release shipped = %v", err)
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read the artifact a release would ship: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("the store holds %q under the shipped key, want %q", got, want)
		}
	})

	t.Run("an image the release declares is a row the plan shows and a push the apply makes", func(t *testing.T) {
		resources := declared(serves)
		if len(resources) == 0 {
			t.Skip("this provider serves no resource primitive, so a release asks for nothing")
		}
		bare := providerkit.StackPlan{Ref: ref, Kind: providerkit.StackInfra, Resources: resources}
		without, err := releaser.Plan(ctx, bare, nil)
		if err != nil {
			t.Fatalf("Plan() of a release pushing no image = %v", err)
		}

		store := &countedImages{}
		pushing := bare
		pushing.Images = providerkit.ImagePlan{Store: store, Pushes: []providerkit.ImagePush{{
			App:    "conformance",
			Source: "ocel/conformance@sha256:" + conformanceImageDigest,
			Target: "registry.invalid/conformance:sha256-" + conformanceImageDigest,
			Digest: "sha256:" + conformanceImageDigest,
		}}}
		with, err := releaser.Plan(ctx, pushing, nil)
		if err != nil {
			requireInvalid(t, err, "Plan")
			if _, err := releaser.Provision(ctx, pushing, nil); err == nil {
				t.Error("Plan() refused a declared image push and Provision() took it, so the apply pushed nothing and said so to nobody")
			}
			return
		}
		if planRows(t, with, "Plan") <= planRows(t, without, "Plan") {
			t.Error("declaring an image push added no plan row, and a push is a write to the customer's registry like any other: " +
				"the plan and the apply must ship it down one path")
		}
		if pushed := store.Pushed(); pushed != 0 {
			t.Errorf("Plan() pushed %d images, and a plan is the diff a human consents to before anything moves", pushed)
		}

		if _, err := releaser.Provision(ctx, pushing, nil); err != nil {
			t.Fatalf("Provision() of the release whose plan showed the image = %v", err)
		}
		if pushed := store.Pushed(); pushed != 1 {
			t.Errorf("Provision() pushed %d images, want the one the plan showed: the plan promised a push the apply never made", pushed)
		}
	})

	t.Run("every link a plan asks for comes back carrying the properties its type promises", func(t *testing.T) {
		resources := declared(serves)
		if len(resources) == 0 {
			t.Skip("this provider serves no resource primitive, so a plan can ask for nothing")
		}
		result, err := releaser.Provision(ctx, providerkit.StackPlan{
			Ref:       ref,
			Kind:      providerkit.StackInfra,
			Resources: resources,
		}, nil)
		if err != nil {
			t.Fatalf("Provision() of every primitive this provider serves = %v", err)
		}
		if len(result.Links) != len(resources) {
			t.Fatalf("Provision() returned %d links for %d resources, and an app binds to each by name", len(result.Links), len(resources))
		}
		for _, link := range result.Links {
			if err := providerkit.VerifyProperties(link); err != nil {
				t.Errorf("Provision() returned a link the kit refuses to record: %v", err)
			}
		}

		removal, err := releaser.PlanDestroy(ctx, ref, nil)
		if err != nil {
			t.Fatalf("PlanDestroy() of the stack just provisioned = %v", err)
		}
		if rows := planRows(t, removal, "PlanDestroy"); rows == 0 {
			t.Error("PlanDestroy() of a stack standing showed nothing going, and a teardown is consented to by what it shows")
		}
		for _, group := range removal.Groups {
			for _, change := range group.Changes {
				if change.Action != providerkit.ActionDelete && change.Action != providerkit.ActionDisableThenDelete {
					t.Errorf("PlanDestroy() shows %s as %q, and a teardown takes everything down", change.Name, change.Action)
				}
			}
		}

		if err := releaser.Destroy(ctx, ref, nil); err != nil {
			t.Fatalf("Destroy() of the stack just provisioned = %v", err)
		}
	})

	t.Run("a refusal names a code the CLI can render", func(t *testing.T) {
		unserved := providerkit.StackPlan{
			Ref:       ref,
			Kind:      providerkit.StackInfra,
			Resources: []providerkit.Resource{{Name: "unserved", Type: "no-such-primitive"}},
		}
		result, err := releaser.Provision(ctx, unserved, nil)
		if err == nil {
			if len(result.Links) == 0 {
				t.Fatal("Provision() of a primitive this provider does not serve stood nothing up and refused nothing, so a release reads as done where nothing happened")
			}
			if derr := releaser.Destroy(ctx, ref, nil); derr != nil {
				t.Fatal(derr)
			}
			t.Skip("this provider stands up a resource of any type, so there is no unserved primitive to refuse")
		}
		var refusal providerkit.Refusal
		if !errors.As(err, &refusal) {
			t.Fatalf("Provision() of a primitive this provider does not serve failed with %v, want a Refusal the CLI can render", err)
		}
		if !slices.Contains(
			[]providerkit.Code{providerkit.CodeInvalid, providerkit.CodeNotReady, providerkit.CodeDenied, providerkit.CodeBusy},
			refusal.Code,
		) {
			t.Errorf("Provision() refused with code %q, which is none the kit maps", refusal.Code)
		}
		if planned, err := releaser.Plan(ctx, unserved, nil); err == nil {
			t.Errorf("Plan() showed %+v for a release its own provision refuses, and the plan is the diff the apply runs", planned.Groups)
		}
	})
}
