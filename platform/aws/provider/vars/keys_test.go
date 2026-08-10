package vars

import (
	"strings"
	"testing"
)

func TestNestedFolderIsNotAddressableAsItsParent(t *testing.T) {
	parent := Coordinate{Slug: "shop", Folder: "/web", Key: "POSTHOG_ID"}.canonical()
	nested := Coordinate{Slug: "shop", Folder: "/web/admin", Key: "POSTHOG_ID"}.canonical()

	if strings.HasPrefix(currentSortKey(nested), folderPrefix(parent.Folder)) {
		t.Errorf("sort key %q begins with the parent folder's prefix %q; nesting must be invisible to any prefix read",
			currentSortKey(nested), folderPrefix(parent.Folder))
	}
}

func TestCurrentAndHistoryOccupySeparateNamespaces(t *testing.T) {
	c := testCoordinate().canonical()

	if strings.HasPrefix(historySortKey(c, 1), currentPrefix) {
		t.Errorf("history sort key %q sits under the current namespace %q", historySortKey(c, 1), currentPrefix)
	}
	if !strings.HasPrefix(currentSortKey(c), currentPrefix) {
		t.Errorf("current sort key %q is not under %q", currentSortKey(c), currentPrefix)
	}
}

func TestHistorySortKeysOrderByVersion(t *testing.T) {
	c := testCoordinate().canonical()
	if !(historySortKey(c, 9) < historySortKey(c, 10)) {
		t.Errorf("version 9 (%q) does not sort before version 10 (%q); history would read out of order",
			historySortKey(c, 9), historySortKey(c, 10))
	}
}

func TestOverridesOfOneKeyClusterTogether(t *testing.T) {
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
}

func TestCurrentSortKeyRoundTrips(t *testing.T) {
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
}

func TestValidateRejectsTheDelimiterInUserChosenNames(t *testing.T) {
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
}

func TestValidateRejectsMalformedFolders(t *testing.T) {
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
}

func TestValidateRejectsTheRootFolderAsASecondSpellingOfRoot(t *testing.T) {
	err := Coordinate{Slug: "shop", Key: "K", Folder: rootFolder}.validate()
	if err == nil {
		t.Fatalf("validate() accepted folder %q, want it rejected as a second spelling of root", rootFolder)
	}
	if !strings.Contains(err.Error(), "leave the folder off") {
		t.Errorf("validate() err = %v, want it to say to leave the folder off instead", err)
	}
}

func TestValidateRequiresSlugAndKey(t *testing.T) {
	if err := (Coordinate{Key: "K"}).validate(); err == nil {
		t.Error("validate() accepted a coordinate with no slug")
	}
	if err := (Coordinate{Slug: "shop"}).validate(); err == nil {
		t.Error("validate() accepted a coordinate with no key")
	}
}

func TestValidateRejectsTheClassWideSentinelAsAnEnvironment(t *testing.T) {
	c := Coordinate{Slug: "shop", Key: "K", Environment: classWideEnvironment}
	if err := c.validate(); err == nil {
		t.Errorf("validate() accepted %q as an environment name; it is the class-wide sentinel", classWideEnvironment)
	}
}
