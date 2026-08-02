// Package vars is the variable store's write and read path: the key structure
// values are addressed by, and the operations an operator or the variables UI
// performs on them. It runs provider-side because the values live in the
// user's own cloud account and only the provider can reach them.
package vars

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// delimiter separates a sort key's type-prefixed components. It is
	// forbidden in every user-chosen name (see Coordinate.validate), which is
	// what makes a key unambiguous to build and to parse back.
	delimiter = "#"

	// classWideEnvironment is the environment component of a value that binds
	// to the whole class rather than to one named preview environment. Every
	// key carries an environment component so a key's overrides sit adjacent to
	// the value they override; this is the component the default occupies.
	classWideEnvironment = "*"

	// rootFolder is the folder component of a value that is not scoped to any
	// app folder. It is a spelling the key structure uses and a caller never
	// does: above the store root is the absence of a folder, so validate
	// rejects it as an incoming folder rather than accepting a second name for
	// the same place.
	rootFolder = "/"

	// currentPrefix and historyPrefixToken open the two sort-key namespaces.
	// They are separate so listing a project's current values never drags its
	// version history along.
	currentPrefix      = "V" + delimiter
	historyPrefixToken = "H" + delimiter

	// historyWindow is how many versions a cell keeps. Each write prunes the
	// version that falls out of it.
	historyWindow = 50

	// MaxValueBytes caps a value's plaintext. It is the largest plaintext KMS
	// encrypts directly, and is far below DynamoDB's item-size limit, so one
	// bound serves both.
	MaxValueBytes = 4096

	// versionDigits zero-pads a version number so history sorts numerically
	// under DynamoDB's lexicographic sort key.
	versionDigits = 10

	// IndexName is the table's reverse-lookup index: the one access path
	// answering what references a value, so the blast radius of an edit is a
	// query rather than a scan. It is sparse — only a reference item carries the
	// attributes it is keyed on — and it is exported for the same reason
	// PartitionKey is: the template that provisions it and the query that reads
	// it must name one index or they drift apart silently.
	IndexName = "gsi1"
)

// Coordinate addresses exactly one cell. The zero values of Folder and
// Environment mean "root" and "class-wide"; canonical renders those into the
// sentinels the key structure uses, so callers never spell them.
type Coordinate struct {
	Slug        string
	Folder      string
	Key         string
	Environment string
}

// String names a coordinate the way a message about one should read: the
// project it belongs to, and the axes it varies along only where it varies.
func (c Coordinate) String() string {
	out := c.Slug + "/" + c.Key
	if c.Folder != "" {
		out += " in " + c.Folder
	}
	if c.Environment != "" {
		out += " for " + c.Environment
	}
	return out
}

func (c Coordinate) canonical() Coordinate {
	if c.Folder == "" {
		c.Folder = rootFolder
	}
	if c.Environment == "" {
		c.Environment = classWideEnvironment
	}
	return c
}

// validate rejects a coordinate the key structure cannot express
// unambiguously. It runs before any write, and before any read that would
// otherwise address a neighbouring cell by accident.
func (c Coordinate) validate() error {
	if c.Slug == "" {
		return fmt.Errorf("a project slug is required")
	}
	if c.Key == "" {
		return fmt.Errorf("a variable name is required")
	}
	if c.Environment == classWideEnvironment {
		return fmt.Errorf("%q is reserved: it names the value that binds class-wide", classWideEnvironment)
	}
	for name, component := range map[string]string{
		"project slug":     c.Slug,
		"variable name":    c.Key,
		"folder":           c.Folder,
		"environment name": c.Environment,
	} {
		if strings.Contains(component, delimiter) {
			return fmt.Errorf("%s %q may not contain %q", name, component, delimiter)
		}
	}
	if c.Folder != "" {
		if !strings.HasPrefix(c.Folder, "/") {
			return fmt.Errorf("folder %q must start with %q", c.Folder, "/")
		}
		if c.Folder == rootFolder {
			return fmt.Errorf("folder %q is the project root, which is what an unbound app already reads; leave the folder off instead", c.Folder)
		}
		if strings.HasSuffix(c.Folder, "/") {
			return fmt.Errorf("folder %q must not end with %q", c.Folder, "/")
		}
		if strings.Contains(c.Folder, "//") {
			return fmt.Errorf("folder %q has an empty path segment", c.Folder)
		}
	}
	return nil
}

// PartitionKey partitions the table per project and per env class, so every
// operation is scoped to one project's values and a value can never be read
// across the class boundary its key enforces. It is exported because a grant
// is scoped to it too: a deploy conditions a function's read on this exact
// prefix, and the condition and the key it constrains must be built by the
// same function or they drift apart silently.
func PartitionKey(slug, class string) string {
	return "P" + delimiter + slug + delimiter + "C" + delimiter + class
}

// parsePartitionKey recovers the project a partition belongs to. A reference
// carries its target's partition key, so reading one back is how the reverse
// index answers with a coordinate rather than with an opaque address.
func parsePartitionKey(pk string) (string, error) {
	parts := strings.Split(pk, delimiter)
	if len(parts) != 4 || parts[0] != "P" || parts[2] != "C" {
		return "", fmt.Errorf("unrecognized partition key %q", pk)
	}
	return parts[1], nil
}

// folderPrefix opens the components of one folder, terminated by the
// delimiter. The terminator is what makes nesting organisational only: "/web"
// is not a prefix of "/web/admin" once both are closed, so no prefix read can
// reach into a folder it did not name.
func folderPrefix(folder string) string {
	return "F" + delimiter + folder + delimiter
}

// keyPrefix opens one key within one folder. A key's named-environment
// overrides all sit under it, which is why the environment component comes
// after the key rather than before it.
func keyPrefix(c Coordinate) string {
	return currentPrefix + folderPrefix(c.Folder) + "K" + delimiter + c.Key + delimiter
}

func cellSuffix(c Coordinate) string {
	return folderPrefix(c.Folder) + "K" + delimiter + c.Key + delimiter + "E" + delimiter + c.Environment
}

func currentSortKey(c Coordinate) string {
	return currentPrefix + cellSuffix(c)
}

func historySortKey(c Coordinate, version int64) string {
	return historyPrefix(c) + fmt.Sprintf("%0*d", versionDigits, version)
}

func historyPrefix(c Coordinate) string {
	return historyPrefixToken + cellSuffix(c) + delimiter + "N" + delimiter
}

// parseCurrentSortKey recovers the coordinate a current-value item is stored
// at. The key is the single source of a cell's address: mirroring its
// components into attributes would let the two disagree.
func parseCurrentSortKey(slug, sk string) (Coordinate, error) {
	parts := strings.Split(sk, delimiter)
	if len(parts) != 7 || parts[0] != "V" || parts[1] != "F" || parts[3] != "K" || parts[5] != "E" {
		return Coordinate{}, fmt.Errorf("unrecognized value key %q", sk)
	}
	c := Coordinate{Slug: slug, Folder: parts[2], Key: parts[4], Environment: parts[6]}
	if c.Folder == rootFolder {
		c.Folder = ""
	}
	if c.Environment == classWideEnvironment {
		c.Environment = ""
	}
	return c, nil
}

// parseHistorySortKey recovers a history item's version number.
func parseHistorySortKey(sk string) (int64, error) {
	parts := strings.Split(sk, delimiter)
	if len(parts) != 9 || parts[0] != "H" || parts[7] != "N" {
		return 0, fmt.Errorf("unrecognized version key %q", sk)
	}
	return strconv.ParseInt(parts[8], 10, 64)
}
