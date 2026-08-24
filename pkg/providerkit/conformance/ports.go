package conformance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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

	t.Run("RecordStore", func(t *testing.T) { RunRecordStore(t, provider.Records()) })
	t.Run("Sealer", func(t *testing.T) { RunSealer(t, provider.Sealer()) })
	t.Run("Bootstrapper", func(t *testing.T) { RunBootstrapper(t, bootstrapperOf(t, provider)) })
	t.Run("ArtifactStore", func(t *testing.T) { RunArtifactStore(t, provider.Artifacts()) })
	t.Run("Releaser", func(t *testing.T) { RunReleaser(t, provider.Releases(), provider.Serves()) })
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

		removals, err := bootstrapper.Removals(ctx, class)
		if err != nil {
			t.Fatalf("Removals() = %v", err)
		}
		for _, removal := range removals {
			if removal.Kind == "" || removal.Name == "" {
				t.Errorf("Removals() returned %+v, and a removal plan cannot render a nameless item", removal)
			}
			if !edge.ValidSurfaceAction(removal.Action) {
				t.Errorf("Removals() returned action %q, which is none the plan knows", removal.Action)
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
		if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Drop: ordered}, nil); err != nil {
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

	t.Run("a policy is rendered for either tier", func(t *testing.T) {
		for _, tier := range []providerkit.CredentialTier{providerkit.TierBootstrap, providerkit.TierDeploy} {
			if _, err := credentials.Policy(tier); err != nil {
				t.Errorf("Policy(%s) = %v, want the permissions that tier needs", tier, err)
			}
		}
	})
}

func RunArtifactStore(t *testing.T, artifacts providerkit.ArtifactStore) {
	t.Helper()

	ctx := context.Background()
	ref := providerkit.ArtifactRef{Class: providerkit.ClassProduction, Bucket: providerkit.StoreFunctions, Key: "conformance/" + t.Name() + "/bundle.zip"}
	body := []byte("a build artifact")

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

func RunReleaser(t *testing.T, releaser providerkit.Releaser, serves []providerkit.LinkType) {
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

	t.Run("every link a plan asks for comes back carrying the properties its type promises", func(t *testing.T) {
		var resources []providerkit.Resource
		for _, kind := range serves {
			resources = append(resources, providerkit.Resource{Name: "c-" + string(kind), Type: kind})
		}
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
		if err := releaser.Destroy(ctx, ref, nil); err != nil {
			t.Fatalf("Destroy() of the stack just provisioned = %v", err)
		}
	})

	t.Run("a refusal names a code the CLI can render", func(t *testing.T) {
		_, err := releaser.Provision(ctx, providerkit.StackPlan{
			Ref:       ref,
			Kind:      providerkit.StackInfra,
			Resources: []providerkit.Resource{{Name: "unserved", Type: "no-such-primitive"}},
		}, nil)
		if err == nil {
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
	})
}
