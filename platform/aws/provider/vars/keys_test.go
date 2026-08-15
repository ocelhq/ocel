package vars

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
)

func TestKeys(t *testing.T) {
	t.Parallel()

	t.Run("nested folder is not addressable as its parent", func(t *testing.T) {
		t.Parallel()

		parent := Coordinate{Slug: "shop", Folder: "/web", Key: "POSTHOG_ID"}.canonical()
		nested := Coordinate{Slug: "shop", Folder: "/web/admin", Key: "POSTHOG_ID"}.canonical()

		if strings.HasPrefix(currentSortKey(nested), folderPrefix(parent.Folder)) {
			t.Errorf("sort key %q begins with the parent folder's prefix %q; nesting must be invisible to any prefix read",
				currentSortKey(nested), folderPrefix(parent.Folder))
		}
	})

	t.Run("current and history occupy separate namespaces", func(t *testing.T) {
		t.Parallel()

		c := testCoordinate().canonical()

		if strings.HasPrefix(historySortKey(c, 1), currentPrefix) {
			t.Errorf("history sort key %q sits under the current namespace %q", historySortKey(c, 1), currentPrefix)
		}
		if !strings.HasPrefix(currentSortKey(c), currentPrefix) {
			t.Errorf("current sort key %q is not under %q", currentSortKey(c), currentPrefix)
		}
	})

	t.Run("history sort keys order by version", func(t *testing.T) {
		t.Parallel()

		c := testCoordinate().canonical()
		if !(historySortKey(c, 9) < historySortKey(c, 10)) {
			t.Errorf("version 9 (%q) does not sort before version 10 (%q); history would read out of order",
				historySortKey(c, 9), historySortKey(c, 10))
		}
	})

	t.Run("overrides of one key cluster together", func(t *testing.T) {
		t.Parallel()

		c := Coordinate{Slug: "shop", Key: "STRIPE_API_KEY"}.canonical()
		override := Coordinate{Slug: "shop", Key: "STRIPE_API_KEY", Environment: "staging"}.canonical()
		other := Coordinate{Slug: "shop", Key: "STRIPE_WEBHOOK_SECRET"}.canonical()

		prefix := keyPrefix(c)
		if !strings.HasPrefix(currentSortKey(override), prefix) {
			t.Errorf("override %q does not cluster under its key's prefix %q", currentSortKey(override), prefix)
		}
		if strings.HasPrefix(currentSortKey(other), prefix) {
			t.Errorf("unrelated key %q clusters under %q", currentSortKey(other), prefix)
		}
	})

	t.Run("current sort key round trips", func(t *testing.T) {
		t.Parallel()

		for _, c := range []Coordinate{
			{Slug: "shop", Key: "STRIPE_API_KEY"},
			{Slug: "shop", Key: "POSTHOG_ID", Folder: "/web/admin"},
			{Slug: "shop", Key: "POSTHOG_ID", Environment: "staging"},
		} {
			got, err := parseCurrentSortKey(c.Slug, currentSortKey(c.canonical()))
			if err != nil {
				t.Fatalf("parseCurrentSortKey(%+v) err = %v", c, err)
			}
			if got != c {
				t.Errorf("round trip of %+v = %+v", c, got)
			}
		}
	})
}

func TestKeysCarryTheProject(t *testing.T) {
	t.Parallel()

	t.Run("every key a cell is written under names its project", func(t *testing.T) {
		t.Parallel()

		for _, class := range []string{"production", "preview"} {
			for _, slug := range []string{"shop", "platform", "billing-eu"} {
				c := Coordinate{Slug: slug, Key: "STRIPE_API_KEY", Folder: "/web", Environment: "staging"}.canonical()
				for _, key := range []string{
					PartitionKey(slug, class),
					PartitionKey(slug, class) + delimiter + currentSortKey(c),
					PartitionKey(slug, class) + delimiter + historySortKey(c, 3),
				} {
					got, err := naming.ProjectOf(key)
					if err != nil {
						t.Fatalf("naming.ProjectOf(%q) err = %v", key, err)
					}
					if got != slug {
						t.Errorf("naming.ProjectOf(%q) = %q, want %q", key, got, slug)
					}
				}
			}
		}
	})

	t.Run("partition key round trips", func(t *testing.T) {
		t.Parallel()

		for _, slug := range []string{"shop", "billing-eu"} {
			pk := PartitionKey(slug, "production")
			got, err := parsePartitionKey(pk)
			if err != nil {
				t.Fatalf("parsePartitionKey(%q) err = %v", pk, err)
			}
			if got != slug {
				t.Errorf("parsePartitionKey(%q) = %q, want %q", pk, got, slug)
			}
		}
	})

	t.Run("rejects keys of a neighbouring grammar", func(t *testing.T) {
		t.Parallel()

		for _, pk := range []string{
			"",
			"shop",
			naming.ProjectKey("shop"),
			naming.ISRTagKey("shop", naming.AppStack("prod", "web", naming.NewRelease("b1", "")), "products"),
			PartitionKey("shop", "production") + delimiter + "extra",
		} {
			if _, err := parsePartitionKey(pk); err == nil {
				t.Errorf("parsePartitionKey(%q) was accepted as a vars partition key", pk)
			}
		}
	})
}

func TestValidate(t *testing.T) {
	t.Parallel()

	t.Run("rejects the delimiter in user chosen names", func(t *testing.T) {
		t.Parallel()

		for name, c := range map[string]Coordinate{
			"slug":        {Slug: "sh#op", Key: "K"},
			"key":         {Slug: "shop", Key: "STRIPE#KEY"},
			"folder":      {Slug: "shop", Key: "K", Folder: "/we#b"},
			"environment": {Slug: "shop", Key: "K", Environment: "sta#ging"},
		} {
			if err := c.validate(); err == nil {
				t.Errorf("validate() accepted %s containing the key delimiter", name)
			}
		}
	})

	t.Run("rejects malformed folders", func(t *testing.T) {
		t.Parallel()

		for _, folder := range []string{"web", "/web/", "//web"} {
			c := Coordinate{Slug: "shop", Key: "K", Folder: folder}
			if err := c.validate(); err == nil {
				t.Errorf("validate() accepted folder %q", folder)
			}
		}
		for _, folder := range []string{"", "/web", "/web/admin"} {
			c := Coordinate{Slug: "shop", Key: "K", Folder: folder}
			if err := c.validate(); err != nil {
				t.Errorf("validate() rejected folder %q: %v", folder, err)
			}
		}
	})

	t.Run("rejects the root folder as a second spelling of root", func(t *testing.T) {
		t.Parallel()

		err := Coordinate{Slug: "shop", Key: "K", Folder: rootFolder}.validate()
		if err == nil {
			t.Fatalf("validate() accepted folder %q, want it rejected as a second spelling of root", rootFolder)
		}
		if !strings.Contains(err.Error(), "leave the folder off") {
			t.Errorf("validate() err = %v, want it to say to leave the folder off instead", err)
		}
	})

	t.Run("requires slug and key", func(t *testing.T) {
		t.Parallel()

		if err := (Coordinate{Key: "K"}).validate(); err == nil {
			t.Error("validate() accepted a coordinate with no slug")
		}
		if err := (Coordinate{Slug: "shop"}).validate(); err == nil {
			t.Error("validate() accepted a coordinate with no key")
		}
	})

	t.Run("rejects the class wide sentinel as an environment", func(t *testing.T) {
		t.Parallel()

		c := Coordinate{Slug: "shop", Key: "K", Environment: ClassWideEnvironment}
		if err := c.validate(); err == nil {
			t.Errorf("validate() accepted %q as an environment name; it is the class-wide sentinel", ClassWideEnvironment)
		}
	})
}
