package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func runPorts(t *testing.T, suite Suite) {
	t.Helper()

	if suite.New == nil {
		t.Skip("the suite has no constructor, so there are no ports to exercise")
	}
	p, err := suite.New(context.Background(), provider.Settings{Options: suite.Options})
	if err != nil {
		t.Fatalf("New() error = %v, want a provider", err)
	}
	RunPorts(t, p)
}

func RunPorts(t *testing.T, p provider.Provider) {
	t.Helper()

	t.Run("Store", func(t *testing.T) { RunStore(t, p.KeyValues()) })
	t.Run("Cipher", func(t *testing.T) { RunCipher(t, p.Cipher()) })
	facts := p.Facts()
	t.Run("ArtifactStore", func(t *testing.T) { RunArtifactStore(t, facts, p.Artifacts()) })
	t.Run("Stacks", func(t *testing.T) {
		RunStacks(t, facts, p.Stacks(), p.Artifacts(), p.KeyValues())
	})
	t.Run("Bootstrap", func(t *testing.T) {
		RunBootstrap(t, bootstrapOf(t, p), facts.DefaultEdge)
	})
	t.Run("Credentials", func(t *testing.T) { RunCredentials(t, p.Credentials()) })
	t.Run("Edges", func(t *testing.T) { RunEdges(t, facts, p.Edges()) })
	t.Run("Routers", func(t *testing.T) { RunRouters(t, facts, p.Edges(), p.Routers()) })
	t.Run("DNS", func(t *testing.T) { RunDNS(t, facts, p.DNS()) })
	t.Run("Workers", func(t *testing.T) { RunWorkers(t, facts) })
	t.Run("KVStores", func(t *testing.T) { RunKVStores(t, facts) })
	t.Run("Realtime", func(t *testing.T) { RunRealtime(t, facts) })
}

func bootstrapOf(t *testing.T, p provider.Provider) provider.Bootstrap {
	t.Helper()
	bootstrap, err := p.Bootstrap(p.Facts().DefaultEdge)
	if err != nil {
		t.Fatalf("Bootstrap(%q) error = %v, want the bootstrap for this provider's default edge", p.Facts().DefaultEdge, err)
	}
	return bootstrap
}

func conformanceIn(tier environment.Tier, t *testing.T) keyvalue.Partition {
	return keyvalue.Partition{Tier: tier, Root: keyvalue.RootConformance, Path: []string{t.Name()}}
}

func under(t *testing.T, path ...string) keyvalue.Key {
	return conformanceIn(environment.TierProduction, t).Key(path...)
}

func text(value string) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

func RunStore(t *testing.T, store keyvalue.Store) {
	t.Helper()

	ctx := context.Background()

	t.Run("an unwritten key is no entry", func(t *testing.T) {
		if _, err := store.Read(ctx, under(t, "never-written")); !errors.Is(err, keyvalue.ErrNotFound) {
			t.Fatalf("Read() of a key never written = %v, want ErrNotFound", err)
		}
	})

	t.Run("a first write claims the key", func(t *testing.T) {
		key := under(t, "claimed")
		revision, err := store.Write(ctx, keyvalue.Entry{Key: key, Value: text("one")})
		if err != nil {
			t.Fatalf("Write() of a new entry = %v, want it stored", err)
		}
		if revision == "" {
			t.Fatal("Write() returned an empty revision, and a compare-and-set has nothing to compare")
		}
		recorded, err := store.Read(ctx, key)
		if err != nil || !bytes.Equal(recorded.Value, text("one")) {
			t.Fatalf("Read() = %s, %v, want the value just written", recorded.Value, err)
		}
		if recorded.Revision != revision {
			t.Fatalf("Read() revision = %q, want the %q Write() reported", recorded.Revision, revision)
		}
	})

	t.Run("a value that is not JSON is refused and nothing lands", func(t *testing.T) {
		key, beside := under(t, "unreadable"), under(t, "beside")
		var refused refusal.Refusal
		if _, err := store.Write(ctx, keyvalue.Entry{Key: key, Value: json.RawMessage("one")}); !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
			t.Fatalf("Write() of a value that is not JSON = %v, want an %s refusal", err, refusal.CodeInvalid)
		}
		if err := store.WritePair(ctx,
			keyvalue.Entry{Key: beside, Value: text("one")},
			keyvalue.Entry{Key: key, Value: json.RawMessage("{")},
		); !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
			t.Fatalf("WritePair() where one value is not JSON = %v, want an %s refusal", err, refusal.CodeInvalid)
		}
		for _, key := range []keyvalue.Key{key, beside} {
			if _, err := store.Read(ctx, key); !errors.Is(err, keyvalue.ErrNotFound) {
				t.Errorf("Read(%s) after a refused write = %v, want ErrNotFound", key, err)
			}
		}
	})

	t.Run("a second write at the same key must name a revision", func(t *testing.T) {
		key := under(t, "occupied")
		if _, err := store.Write(ctx, keyvalue.Entry{Key: key, Value: text("one")}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Write(ctx, keyvalue.Entry{Key: key, Value: text("two")}); !errors.Is(err, keyvalue.ErrStale) {
			t.Fatalf("Write() at a taken key with no revision = %v, want ErrStale", err)
		}
	})

	t.Run("a write at the revision read wins and a later one loses", func(t *testing.T) {
		key := under(t, "compared")
		if _, err := store.Write(ctx, keyvalue.Entry{Key: key, Value: text("one")}); err != nil {
			t.Fatal(err)
		}
		recorded, err := store.Read(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		recorded.Value = text("two")
		if _, err := store.Write(ctx, recorded); err != nil {
			t.Fatalf("Write() at the revision read = %v, want it stored", err)
		}
		recorded.Value = text("three")
		if _, err := store.Write(ctx, recorded); !errors.Is(err, keyvalue.ErrStale) {
			t.Fatalf("a second write at a revision that moved = %v, want ErrStale", err)
		}
	})

	t.Run("a pair lands whole or not at all", func(t *testing.T) {
		record, value := under(t, "pair", "record"), under(t, "pair", "value")
		if err := store.WritePair(ctx,
			keyvalue.Entry{Key: record, Value: text("one")},
			keyvalue.Entry{Key: value, Value: text("one")},
		); err != nil {
			t.Fatalf("WritePair() of two new entries = %v, want both stored", err)
		}
		recorded, err := store.Read(ctx, record)
		if err != nil {
			t.Fatal(err)
		}
		beside, err := store.Read(ctx, value)
		if err != nil {
			t.Fatal(err)
		}

		moved := beside
		moved.Revision = "a revision nobody wrote"
		recorded.Value, moved.Value = text("two"), text("two")
		if err := store.WritePair(ctx, recorded, moved); !errors.Is(err, keyvalue.ErrStale) {
			t.Fatalf("WritePair() where one half moved = %v, want ErrStale", err)
		}
		for _, key := range []keyvalue.Key{record, value} {
			recorded, err := store.Read(ctx, key)
			if err != nil || !bytes.Equal(recorded.Value, text("one")) {
				t.Fatalf("Read(%s) after a refused pair write = %s, %v, want the value from the write that landed", key, recorded.Value, err)
			}
		}
	})

	t.Run("a removal names the revision it read", func(t *testing.T) {
		key := under(t, "removed")
		revision, err := store.Write(ctx, keyvalue.Entry{Key: key, Value: text("one")})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Remove(ctx, key, "a revision nobody wrote"); !errors.Is(err, keyvalue.ErrStale) {
			t.Fatalf("Remove() at a revision that was never current = %v, want ErrStale", err)
		}
		if err := store.Remove(ctx, key, revision); err != nil {
			t.Fatalf("Remove() at the current revision = %v, want it gone", err)
		}
		if _, err := store.Read(ctx, key); !errors.Is(err, keyvalue.ErrNotFound) {
			t.Fatalf("Read() after Remove() = %v, want ErrNotFound", err)
		}
	})

	t.Run("one tier's entries are not the other's", func(t *testing.T) {
		production, preview := conformanceIn(environment.TierProduction, t).Key("isolated"), conformanceIn(environment.TierPreview, t).Key("isolated")
		if _, err := store.Write(ctx, keyvalue.Entry{Key: production, Value: text("production")}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Read(ctx, preview); !errors.Is(err, keyvalue.ErrNotFound) {
			t.Fatalf("Read() of the preview key after only production was written = %v, want ErrNotFound", err)
		}
		if _, err := store.Write(ctx, keyvalue.Entry{Key: preview, Value: text("preview")}); err != nil {
			t.Fatal(err)
		}
		recorded, err := store.Read(ctx, production)
		if err != nil || !bytes.Equal(recorded.Value, text("production")) {
			t.Fatalf("Read() of the production key = %s, %v, want the production value untouched", recorded.Value, err)
		}
	})

	t.Run("List answers with everything under a prefix", func(t *testing.T) {
		leaves := []keyvalue.Key{
			under(t, "tree"),
			under(t, "tree", "a"),
			under(t, "tree", "b", "one"),
			under(t, "tree", "b", "two"),
		}
		for _, key := range leaves {
			if _, err := store.Write(ctx, keyvalue.Entry{Key: key, Value: text(key.String())}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.Write(ctx, keyvalue.Entry{Key: under(t, "treeish"), Value: text("not under it")}); err != nil {
			t.Fatal(err)
		}

		listed, err := store.List(ctx, conformanceIn(environment.TierProduction, t), "tree")
		if err != nil {
			t.Fatalf("List() = %v", err)
		}
		if len(listed) != len(leaves) {
			t.Fatalf("List() returned %d entries, want the %d written at and under the prefix and nothing beside them", len(listed), len(leaves))
		}
		for _, entry := range listed {
			if !bytes.Equal(entry.Value, text(entry.Key.String())) {
				t.Errorf("List() returned %s containing %s, want the value written at that key", entry.Key, entry.Value)
			}
			if entry.Revision == "" {
				t.Errorf("List() returned %s with no revision, and a caller cannot then remove it", entry.Key)
			}
		}

		deeper, err := store.List(ctx, conformanceIn(environment.TierProduction, t), "tree", "b")
		if err != nil || len(deeper) != 2 {
			t.Fatalf("List() of a deeper prefix returned %d entries, %v, want 2", len(deeper), err)
		}

		whole, err := store.List(ctx, conformanceIn(environment.TierProduction, t))
		if err != nil || len(whole) != len(leaves)+1 {
			t.Fatalf("List() of the partition this suite writes in returned %d entries, %v, want the %d written in it", len(whole), err, len(leaves)+1)
		}
	})

	t.Run("List answers from its own partition and none that extends it", func(t *testing.T) {
		in := conformanceIn(environment.TierProduction, t)
		deeper := keyvalue.Partition{Tier: in.Tier, Root: in.Root, Path: append(slices.Clone(in.Path), "tree")}
		if _, err := store.Write(ctx, keyvalue.Entry{Key: in.Key("tree", "own"), Value: text("own")}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Write(ctx, keyvalue.Entry{Key: deeper.Key("own"), Value: text("deeper")}); err != nil {
			t.Fatal(err)
		}

		for _, c := range []struct {
			in   keyvalue.Partition
			want string
		}{{in, "own"}, {deeper, "deeper"}} {
			listed, err := store.List(ctx, c.in)
			if err != nil || len(listed) != 1 || !bytes.Equal(listed[0].Value, text(c.want)) {
				t.Fatalf("List(%s) = %v, %v, want the one entry written in that partition", c.in, listed, err)
			}
		}
	})

	t.Run("a segment is kept whole whatever it contains", func(t *testing.T) {
		segments := []string{"ocel/web@sha256:0123", "a#b", "100%", "a|b", "a+b", ".hidden", "..", "tab\there", "é"}
		for _, segment := range segments {
			if _, err := store.Write(ctx, keyvalue.Entry{Key: under(t, "builds", segment), Value: text(segment)}); err != nil {
				t.Fatalf("Write() at a segment %q = %v", segment, err)
			}
		}

		listed, err := store.List(ctx, conformanceIn(environment.TierProduction, t), "builds")
		if err != nil {
			t.Fatalf("List() = %v", err)
		}
		var got []string
		for _, entry := range listed {
			rest, named := entry.Key.Under("builds")
			if !named || len(rest) != 1 {
				t.Fatalf("List() returned %s, want one segment beneath builds", entry.Key)
			}
			if !bytes.Equal(entry.Value, text(rest[0])) {
				t.Errorf("List() returned %s containing %s, want the value written at that segment", entry.Key, entry.Value)
			}
			got = append(got, rest[0])
		}
		slices.Sort(got)
		want := slices.Sorted(slices.Values(segments))
		if !slices.Equal(got, want) {
			t.Fatalf("List() named segments %q, want %q", got, want)
		}
	})
}

func RunCipher(t *testing.T, cipher seal.Cipher) {
	t.Helper()

	ctx := context.Background()

	tier := environment.TierProduction
	bound := seal.AssociatedData{
		{Name: "project", Value: "shop"},
		{Name: "environment", Value: "*"},
		{Name: "folder", Value: "/"},
		{Name: "binding", Value: ""},
		{Name: "key", Value: "DATABASE_URL"},
	}
	plaintext := []byte("postgres://example")

	sealed, err := cipher.Seal(ctx, tier, bound, plaintext)
	if err != nil {
		t.Fatalf("Seal() = %v", err)
	}
	if bytes.Contains(sealed, plaintext) {
		t.Fatal("Seal() returned the plaintext inside its output")
	}

	opened, err := cipher.Open(ctx, tier, bound, sealed)
	if err != nil {
		t.Fatalf("Open() under the tier and associated data sealed = %v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("Open() = %q, want %q", opened, plaintext)
	}

	t.Run("a value sealed under one tier does not open under another", func(t *testing.T) {
		if _, err := cipher.Open(ctx, environment.TierPreview, bound, sealed); err == nil {
			t.Fatal("Open() under another tier succeeded, so one tier's key opens another's values")
		}
	})
	for i, field := range bound {
		t.Run("a value sealed here does not open with another "+field.Name, func(t *testing.T) {
			moved := slices.Clone(bound)
			moved[i].Value = field.Value + "moved"
			if _, err := cipher.Open(ctx, tier, moved, sealed); err == nil {
				t.Fatalf("Open() with %s %q succeeded, so the associated data is not authenticated", field.Name, moved[i].Value)
			}
		})
	}
}

func RunBootstrap(t *testing.T, bootstrap provider.Bootstrap, kind edge.Kind) {
	t.Helper()

	ctx := context.Background()
	catalogue := bootstrap.Catalogue()
	wanted, err := applicable(catalogue, kind)
	if err != nil {
		t.Fatalf("the features the %q edge installs = %v", kind, err)
	}

	t.Run("every feature is one the catalogue can install", func(t *testing.T) {
		named := make([]string, 0, len(catalogue))
		for _, f := range catalogue {
			if f.Name == "" {
				t.Error("the catalogue has a feature with no name, and nothing can ask for it")
			}
			named = append(named, f.Name)
		}
		for _, f := range catalogue {
			for _, dep := range f.DependsOn {
				if !slices.Contains(named, dep) {
					t.Errorf("%s depends on %q, which this provider does not offer", f.Name, dep)
				}
			}
		}
		if _, err := bootstrapplan.FeatureLevels(catalogue, named); err != nil {
			t.Fatalf("FeatureLevels() over the whole catalogue = %v, want an order that installs every feature", err)
		}
	})

	t.Run("Describe answers for the tier it was asked about", func(t *testing.T) {
		for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
			described, err := bootstrap.Describe(ctx, tier)
			if err != nil {
				t.Fatalf("Describe(%s) = %v", tier, err)
			}
			if described.Tier != tier {
				t.Errorf("Describe(%s) answered for %s", tier, described.Tier)
			}
			for _, stack := range described.Stacks {
				if stack.Name == "" {
					t.Errorf("Describe(%s) returned a stack with no name, and no plan can name it", tier)
				}
			}
		}
	})

	t.Run("Plan answers for the request Apply would be given", func(t *testing.T) {
		tier := environment.TierProduction
		described, err := bootstrap.Describe(ctx, tier)
		if err != nil {
			t.Fatalf("Describe(%s) = %v", tier, err)
		}
		plan, err := bootstrap.Plan(ctx, provider.BootstrapRequest{Tier: tier})
		if err != nil {
			t.Fatalf("Plan(%s) = %v", tier, err)
		}
		creates := 0
		for _, group := range plan.Groups {
			if group.Name == "" {
				t.Errorf("Plan() returned %+v, and no plan can render a nameless group", group)
			}
			if !provider.ValidChangeAction(group.Action) {
				t.Errorf("Plan() returned group action %q, which is none the plan knows", group.Action)
			}
			if group.Action == provider.ActionUpdate && len(group.Changes) == 0 && group.Reason == "" {
				t.Errorf("Plan() returned %q as an update with neither children nor a reason, which reads as no change at all", group.Name)
			}
			if group.Action == provider.ActionCreate {
				creates++
			}
			for _, change := range group.Changes {
				if change.Name == "" {
					t.Errorf("Plan() returned %+v under %q, and no plan can render a nameless change", change, group.Name)
				}
				if !provider.ValidChangeAction(change.Action) {
					t.Errorf("Plan() returned change action %q, which is none the plan knows", change.Action)
				}
			}
		}
		if !described.Present && creates == 0 {
			t.Error("Plan() against an account with no bootstrap creates nothing, and an apply from here would create the whole bootstrap")
		}
	})

	t.Run("Plan shows a dropped feature leaving", func(t *testing.T) {
		if len(wanted) == 0 {
			t.Skipf("the %q edge installs no feature of this provider, so nothing can be dropped", kind)
		}
		tier := environment.TierProduction
		drop := wanted
		plan, err := bootstrap.Plan(ctx, provider.BootstrapRequest{Tier: tier, Remove: drop})
		if err != nil {
			t.Fatalf("Plan(%s, drop %v) = %v", tier, drop, err)
		}
		leaving := map[string]provider.ChangeAction{}
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
			if action != provider.ActionDelete {
				t.Errorf("Plan() shows %q as %q though it was dropped, and a drop takes its stack down", name, action)
			}
		}
	})

	t.Run("what Apply installs, Describe reports and Remove takes down", func(t *testing.T) {
		tier := environment.TierPreview
		raising, err := ordered(catalogue, wanted)
		if err != nil {
			t.Fatal(err)
		}
		if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, Features: raising}, nil); err != nil {
			t.Fatalf("Apply() of what the %q edge installs (%v) = %v", kind, raising, err)
		}

		described, err := bootstrap.Describe(ctx, tier)
		if err != nil {
			t.Fatalf("Describe() after Apply() = %v", err)
		}
		for _, stack := range described.Stacks {
			if stack.Feature != "" && !slices.Contains(raising, stack.Feature) {
				t.Errorf("Describe() reports a stack for %q, which Apply() was never asked for", stack.Feature)
			}
		}

		removal, err := bootstrap.PlanRemove(ctx, tier)
		if err != nil {
			t.Fatalf("PlanRemove() = %v", err)
		}
		for _, group := range removal.Groups {
			if group.Kind == "" || group.Name == "" {
				t.Errorf("PlanRemove() returned %+v, and a removal plan cannot render a nameless group", group)
			}
			if !provider.ValidChangeAction(group.Action) {
				t.Errorf("PlanRemove() returned action %q, which is none the plan knows", group.Action)
			}
			for _, change := range group.Changes {
				if change.Kind == "" || change.Name == "" {
					t.Errorf("PlanRemove() returned row %+v, and a removal plan cannot render a nameless row", change)
				}
				if !provider.ValidChangeAction(change.Action) {
					t.Errorf("PlanRemove() returned row action %q, which is none the plan knows", change.Action)
				}
			}
		}

		if err := bootstrap.Remove(ctx, tier, nil); err != nil {
			t.Fatalf("Remove() = %v", err)
		}
		gone, err := bootstrap.Describe(ctx, tier)
		if err != nil {
			t.Fatalf("Describe() after Remove() = %v", err)
		}
		if gone.Present {
			t.Error("Describe() still reports the bootstrap present after Remove()")
		}
	})

	t.Run("Apply takes a drop in delete order", func(t *testing.T) {
		tier := environment.TierPreview
		levels, err := bootstrapplan.FeatureLevels(catalogue, wanted)
		if err != nil {
			t.Fatal(err)
		}
		var dropping []string
		for i := len(levels) - 1; i >= 0; i-- {
			dropping = append(dropping, levels[i]...)
		}
		if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, Remove: dropping}, nil); err != nil {
			t.Fatalf("Apply() dropping what the %q edge installs (%v) = %v", kind, dropping, err)
		}
		if err := bootstrap.Remove(ctx, tier, nil); err != nil {
			t.Fatalf("Remove() = %v", err)
		}
	})
}

func applicable(catalogue []provider.Feature, kind edge.Kind) ([]string, error) {
	required, err := bootstrapplan.RequiredFeatures(catalogue, nil, kind)
	if err != nil {
		return nil, err
	}
	applies := map[string]bool{}
	for _, name := range required {
		applies[name] = true
	}
	for _, f := range catalogue {
		if len(f.Frameworks) == 0 && len(f.Edges) == 0 {
			applies[f.Name] = true
		}
	}
	for grew := true; grew; {
		grew = false
		for _, f := range catalogue {
			if !applies[f.Name] {
				continue
			}
			for _, dep := range f.DependsOn {
				if !applies[dep] {
					applies[dep], grew = true, true
				}
			}
		}
	}
	wanted := make([]string, 0, len(applies))
	for _, f := range catalogue {
		if applies[f.Name] {
			wanted = append(wanted, f.Name)
		}
	}
	return wanted, nil
}

func ordered(catalogue []provider.Feature, wanted []string) ([]string, error) {
	levels, err := bootstrapplan.FeatureLevels(catalogue, wanted)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, level := range levels {
		out = append(out, level...)
	}
	return out, nil
}

func RunCredentials(t *testing.T, credentials provider.Credentials) {
	t.Helper()

	ctx := context.Background()

	t.Run("Whoami either says who this is or refuses as denied", func(t *testing.T) {
		principal, err := credentials.Whoami(ctx)
		if err != nil {
			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied {
				t.Fatalf("Whoami() failed with %v, want a Refusal with code %s so the CLI can render a credential problem", err, refusal.CodeDenied)
			}
			if refused.Message == "" {
				t.Error("Whoami() refused with no message, so the CLI has nothing to tell the user")
			}
			return
		}
		if principal.Vendor == "" {
			t.Error("Whoami() answered a principal naming no provider")
		}
		for _, detail := range principal.Details {
			if detail.Label == "" {
				t.Errorf("Whoami() returned a detail with no label: %+v", detail)
			}
		}
	})

	t.Run("permissions are rendered for either purpose, or said not to exist yet", func(t *testing.T) {
		for _, purpose := range []edge.CredentialPurpose{edge.PurposeBootstrap, edge.PurposeDeploy} {
			if err := permissionsRendered(credentials, purpose); err != nil {
				t.Errorf("Permissions(%s) = %v, want the permissions that purpose needs or a %s refusal saying there are none to render yet",
					purpose, err, refusal.CodeNotReady)
			}
		}
	})
}

func permissionsRendered(credentials provider.Credentials, purpose edge.CredentialPurpose) error {
	_, err := credentials.Permissions(purpose)
	var refused refusal.Refusal
	if errors.As(err, &refused) && refused.Code == refusal.CodeNotReady {
		return nil
	}
	return err
}

func RunArtifactStore(t *testing.T, facts provider.Facts, artifacts provider.ArtifactStore) {
	t.Helper()

	ctx := context.Background()
	ref := provider.ArtifactRef{Tier: environment.TierProduction, Bucket: provider.StoreFunctions, Key: "conformance/" + t.Name() + "/bundle.zip"}
	body := []byte("a build artifact")

	if !facts.StoresArtifacts {
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
		present, err := artifacts.Has(ctx, ref)
		if err != nil || !present {
			t.Errorf("Has() of an artifact just put = %v, %v, want true: a deploy re-uploads every unchanged build without it", present, err)
		}
		absent := provider.ArtifactRef{Tier: ref.Tier, Bucket: ref.Bucket, Key: ref.Key + ".never-written"}
		present, err = artifacts.Has(ctx, absent)
		if err != nil {
			t.Errorf("Has() of a key nothing wrote = %v, want a plain false", err)
		}
		if present {
			t.Error("Has() claims a key nothing wrote is stored, so a changed build would never be uploaded")
		}
	})

	t.Run("Open of a key nothing wrote refuses rather than answering empty", func(t *testing.T) {
		absent := provider.ArtifactRef{Tier: ref.Tier, Bucket: ref.Bucket, Key: ref.Key + ".never-written"}
		opened, err := artifacts.Open(ctx, absent)
		if err == nil {
			opened.Close()
			t.Fatal("Open() of a key nothing wrote succeeded, so a missing artifact reads as an empty one")
		}
	})

	t.Run("RemovePrefix takes the prefix and nothing beside it", func(t *testing.T) {
		kept := provider.ArtifactRef{Tier: ref.Tier, Bucket: ref.Bucket, Key: "conformance/" + t.Name() + "-sibling/bundle.zip"}
		if err := artifacts.Put(ctx, kept, bytes.NewReader(body)); err != nil {
			t.Fatal(err)
		}
		if err := artifacts.RemovePrefix(ctx, ref.Tier, "conformance/"+t.Name()+"/", nil); err != nil {
			t.Fatalf("RemovePrefix() = %v", err)
		}
		opened, err := artifacts.Open(ctx, kept)
		if err != nil {
			t.Fatalf("Open() of a key outside the prefix removed = %v, want it still stored", err)
		}
		opened.Close()
	})

	t.Run("RemovePrefix of one tier leaves the other tier's artifacts", func(t *testing.T) {
		key := "conformance/" + t.Name() + "/bundle.zip"
		production := provider.ArtifactRef{Tier: environment.TierProduction, Bucket: ref.Bucket, Key: key}
		preview := provider.ArtifactRef{Tier: environment.TierPreview, Bucket: ref.Bucket, Key: key}
		for _, at := range []provider.ArtifactRef{production, preview} {
			if err := artifacts.Put(ctx, at, bytes.NewReader(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := artifacts.RemovePrefix(ctx, environment.TierPreview, "conformance/"+t.Name()+"/", nil); err != nil {
			t.Fatalf("RemovePrefix() = %v", err)
		}
		opened, err := artifacts.Open(ctx, production)
		if err != nil {
			t.Fatalf("Open() of the production artifact after a preview prefix was removed = %v, want it still stored", err)
		}
		opened.Close()
	})

	t.Run("RemovePrefix of a prefix containing nothing is not an error", func(t *testing.T) {
		if err := artifacts.RemovePrefix(ctx, ref.Tier, "conformance/"+t.Name()+"/nothing-here/", nil); err != nil {
			t.Fatalf("RemovePrefix() of a prefix nothing was written under = %v, want nil", err)
		}
	})
}

func runStorelessArtifactStore(t *testing.T, artifacts provider.ArtifactStore, ref provider.ArtifactRef) {
	t.Helper()

	ctx := context.Background()

	t.Run("Put refuses rather than accepting a write nothing will ever read back", func(t *testing.T) {
		var refused refusal.Refusal
		err := artifacts.Put(ctx, ref, bytes.NewReader([]byte("a build artifact")))
		if !errors.As(err, &refused) {
			t.Fatalf("Put() into a provider that keeps no artifacts = %v, want a refusal: a store that reports a write it loses is worse than one that has none", err)
		}
		if refused.Code != refusal.CodeInvalid {
			t.Errorf("Put() refused with %q, want %q so the CLI renders it as the caller's mistake", refused.Code, refusal.CodeInvalid)
		}
		if refused.Message == "" {
			t.Error("Put() refused with no message, so the CLI has nothing to tell the user")
		}
	})

	t.Run("Open refuses rather than answering empty", func(t *testing.T) {
		var refused refusal.Refusal
		opened, err := artifacts.Open(ctx, ref)
		if !errors.As(err, &refused) {
			if err == nil {
				opened.Close()
			}
			t.Fatalf("Open() from a provider that keeps no artifacts = %v, want a refusal: a missing artifact must never read as an empty one", err)
		}
	})

	t.Run("Has answers a plain false", func(t *testing.T) {
		present, err := artifacts.Has(ctx, ref)
		if err != nil {
			t.Fatalf("Has() = %v, want a plain false: plan synthesis draws its create row from this answer", err)
		}
		if present {
			t.Error("Has() claims a provider that keeps no artifacts has one")
		}
	})

	t.Run("RemovePrefix of any prefix, including one nothing wrote under, is nil", func(t *testing.T) {
		for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
			for _, prefix := range []string{"conformance/" + t.Name() + "/", "conformance/" + t.Name() + "/nothing-here/"} {
				if err := artifacts.RemovePrefix(ctx, tier, prefix, nil); err != nil {
					t.Errorf("RemovePrefix(%s, %q) = %v, want nil: teardown sweeps it on every destroy and every preview reap", tier, prefix, err)
				}
			}
		}
	})
}

func declared(serves []provider.BindingType) []provider.Resource {
	resources := make([]provider.Resource, 0, len(serves))
	for _, kind := range serves {
		resource := provider.Resource{Name: "c-" + string(kind), Declared: "c-" + string(kind), Type: kind}
		switch kind {
		case provider.BindingTopic:
			resource.Topic = &provider.TopicSpec{Consumers: []provider.ConsumerSpec{{Name: "c-consumer", Worker: "c-worker", Retry: provider.ResolveRetryPolicy()}}}
		case provider.BindingTask:
			resource.Topic = &provider.TopicSpec{Consumers: []provider.ConsumerSpec{{Name: resource.Name, Worker: "c-worker", Exclusive: true, Retry: provider.ResolveRetryPolicy()}}}
		}
		resources = append(resources, resource)
	}
	return resources
}

func planRows(t *testing.T, plan provider.Plan, verb string) int {
	t.Helper()

	rows := 0
	for _, group := range plan.Groups {
		if group.Name == "" {
			t.Errorf("%s() returned %+v, and no plan can render a nameless group", verb, group)
		}
		if !provider.ValidChangeAction(group.Action) {
			t.Errorf("%s() returned group action %q, which is none the plan knows", verb, group.Action)
		}
		for _, change := range group.Changes {
			if change.Name == "" {
				t.Errorf("%s() returned %+v under %q, and no plan can render a nameless row", verb, change, group.Name)
			}
			if !provider.ValidChangeAction(change.Action) {
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

func (c *countedImages) Destination() string { return "the counted store" }

func (c *countedImages) Has(context.Context, provider.ImagePush) (bool, error) { return false, nil }

func (c *countedImages) Push(_ context.Context, _ provider.ImagePush, _ progress.Log) error {
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

func RunStacks(t *testing.T, facts provider.Facts, stacks provider.Stacks, artifacts provider.ArtifactStore, store keyvalue.Store) {
	t.Helper()

	ctx := context.Background()
	ref := provider.StackRef{
		Project: "conformance",
		Tier:    environment.TierPreview,
		Name:    naming.InfraStack("conformance"),
	}

	t.Run("Destroy of a stack that was never provisioned is a no-op", func(t *testing.T) {
		absent := ref
		absent.Name = naming.InfraStack("never-provisioned")
		if err := stacks.Destroy(ctx, absent, nil); err != nil {
			t.Fatalf("Destroy() of an absent stack = %v, want nil so a rerun of a teardown is safe", err)
		}
	})

	t.Run("Plan says what a provision would do before it does it", func(t *testing.T) {
		resources := declared(facts.Bindings)
		if len(resources) == 0 {
			t.Skip("this provider serves no resource primitive, so a release asks for nothing")
		}
		planned, err := stacks.Plan(ctx, provider.StackSpec{
			Ref:       ref,
			Kind:      provider.StackInfra,
			Resources: resources,
		}, nil)
		if err != nil {
			t.Fatalf("Plan() of every primitive this provider serves = %v", err)
		}
		if rows := planRows(t, planned, "Plan"); rows == 0 {
			t.Fatal("Plan() of a release provisioning every primitive showed nothing, and the plan is the only thing a human consents to")
		}
	})

	t.Run("an artifact the release must ship is a row the plan shows and an object the apply writes", func(t *testing.T) {
		resources := declared(facts.Bindings)
		if len(resources) == 0 {
			t.Skip("this provider serves no resource primitive, so a release asks for nothing")
		}
		bare := provider.StackSpec{Ref: ref, Kind: provider.StackInfra, Resources: resources}
		without, err := stacks.Plan(ctx, bare, nil)
		if err != nil {
			t.Fatalf("Plan() of a release shipping no artifact = %v", err)
		}

		path := writtenArtifact(t)
		shipping := bare
		shipping.Uploads = []provider.Upload{{
			Name:   "conformance",
			Ref:    provider.ArtifactRef{Tier: ref.Tier, Bucket: provider.StoreFunctions, Key: uploadKey(t)},
			Path:   path,
			Digest: conformanceArtifactDigest,
		}}
		with, err := stacks.Plan(ctx, shipping, nil)
		if err != nil {
			t.Fatalf("Plan() of a release shipping one artifact = %v", err)
		}

		if planRows(t, with, "Plan") <= planRows(t, without, "Plan") {
			t.Error("shipping an artifact added no plan row, and an upload is a mutation of the customer's account like any other: " +
				"the plan and the apply must ship it down one path")
		}

		if !facts.StoresArtifacts {
			if _, err := stacks.Provision(ctx, shipping, nil); err == nil {
				t.Fatal("Provision() shipped an artifact through a provider that keeps no artifact store, " +
					"so the release reported a write that landed nowhere")
			}
			return
		}
		if _, err := stacks.Provision(ctx, shipping, nil); err != nil {
			t.Fatalf("Provision() of the release whose plan showed the artifact = %v", err)
		}
		shipped := shipping.Uploads[0].Ref
		present, err := artifacts.Has(ctx, shipped)
		if err != nil {
			t.Fatalf("Has() of the artifact the release shipped = %v", err)
		}
		if !present {
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
			t.Errorf("the store has %q under the shipped key, want %q", got, want)
		}
	})

	t.Run("an image the release declares is a row the plan shows and a push the apply makes", func(t *testing.T) {
		resources := declared(facts.Bindings)
		if len(resources) == 0 {
			t.Skip("this provider serves no resource primitive, so a release asks for nothing")
		}
		bare := provider.StackSpec{Ref: ref, Kind: provider.StackInfra, Resources: resources}
		without, err := stacks.Plan(ctx, bare, nil)
		if err != nil {
			t.Fatalf("Plan() of a release pushing no image = %v", err)
		}

		store := &countedImages{}
		pushing := bare
		pushing.Images = provider.ImagePushes{Store: store, Pushes: []provider.ImagePush{{
			App:      "conformance",
			Source:   "ocel/conformance@sha256:" + conformanceImageDigest,
			ImageRef: "registry.invalid/conformance:sha256-" + conformanceImageDigest,
			Digest:   "sha256:" + conformanceImageDigest,
		}}}
		with, err := stacks.Plan(ctx, pushing, nil)
		if err != nil {
			requireInvalid(t, err, "Plan")
			if _, err := stacks.Provision(ctx, pushing, nil); err == nil {
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

		if _, err := stacks.Provision(ctx, pushing, nil); err != nil {
			t.Fatalf("Provision() of the release whose plan showed the image = %v", err)
		}
		if pushed := store.Pushed(); pushed != 1 {
			t.Errorf("Provision() pushed %d images, want the one the plan showed: the plan promised a push the apply never made", pushed)
		}
	})

	t.Run("every binding a plan asks for comes back with the properties its type promises", func(t *testing.T) {
		resources := declared(facts.Bindings)
		if len(resources) == 0 {
			t.Skip("this provider serves no resource primitive, so a plan can ask for nothing")
		}
		result, err := stacks.Provision(ctx, provider.StackSpec{
			Ref:       ref,
			Kind:      provider.StackInfra,
			Resources: resources,
		}, nil)
		if err != nil {
			t.Fatalf("Provision() of every primitive this provider serves = %v", err)
		}
		if len(result.Bindings) != len(resources) {
			t.Fatalf("Provision() returned %d bindings for %d resources, and an app binds to each by name", len(result.Bindings), len(resources))
		}
		for _, binding := range result.Bindings {
			if err := provider.VerifyProperties(binding); err != nil {
				t.Errorf("Provision() returned a binding providerserver refuses to record: %v", err)
			}
		}

		if store != nil {
			recorded := stackrecords.Stack{Kind: provider.StackInfra, Bindings: result.Bindings}
			if err := stackrecords.Write(ctx, store, ref.Tier, ref.Project, ref.Name, recorded); err != nil {
				t.Fatalf("recording what the release returned, as providerserver does after every Provision() = %v", err)
			}
			defer func() {
				if err := stackrecords.Forget(ctx, store, ref.Tier, ref.Project, ref.Name); err != nil {
					t.Errorf("forgetting the stack the teardown took = %v", err)
				}
			}()
		}

		removal, err := stacks.PlanDestroy(ctx, ref, nil)
		if err != nil {
			t.Fatalf("PlanDestroy() of the stack just provisioned = %v", err)
		}
		if rows := planRows(t, removal, "PlanDestroy"); rows == 0 {
			t.Error("PlanDestroy() of a provisioned stack showed nothing going, and a teardown is consented to by what it shows")
		}
		for _, group := range removal.Groups {
			for _, change := range group.Changes {
				if change.Action != provider.ActionDelete && change.Action != provider.ActionDisableThenDelete {
					t.Errorf("PlanDestroy() shows %s as %q, and a teardown takes everything down", change.Name, change.Action)
				}
			}
		}

		if err := stacks.Destroy(ctx, ref, nil); err != nil {
			t.Fatalf("Destroy() of the stack just provisioned = %v", err)
		}
	})

	t.Run("a refusal names a code the CLI can render", func(t *testing.T) {
		unserved := provider.StackSpec{
			Ref:       ref,
			Kind:      provider.StackInfra,
			Resources: []provider.Resource{{Name: "unserved", Type: "no-such-primitive"}},
		}
		result, err := stacks.Provision(ctx, unserved, nil)
		if err == nil {
			if len(result.Bindings) == 0 {
				t.Fatal("Provision() of a primitive this provider does not serve provisioned nothing and refused nothing, so a release reads as done where nothing happened")
			}
			if derr := stacks.Destroy(ctx, ref, nil); derr != nil {
				t.Fatal(derr)
			}
			t.Skip("this provider provisions a resource of any type, so there is no unserved primitive to refuse")
		}
		var refused refusal.Refusal
		if !errors.As(err, &refused) {
			t.Fatalf("Provision() of a primitive this provider does not serve failed with %v, want a Refusal the CLI can render", err)
		}
		if !slices.Contains(
			[]refusal.Code{refusal.CodeInvalid, refusal.CodeNotReady, refusal.CodeDenied, refusal.CodeBusy},
			refused.Code,
		) {
			t.Errorf("Provision() refused with code %q, which is none providerserver maps", refused.Code)
		}
		if planned, err := stacks.Plan(ctx, unserved, nil); err == nil {
			t.Errorf("Plan() showed %+v for a release its own provision refuses, and the plan is the diff the apply runs", planned.Groups)
		}
	})
}
