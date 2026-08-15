package vars

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	delimiter = naming.KeySeparator

	ClassWideEnvironment = "*"

	rootFolder = "/"

	tokenValue   = "VALUE"
	tokenHistory = "HISTORY"
	tokenFolder  = "FOLDER"
	tokenName    = "NAME"
	tokenEnv     = "ENV"
	tokenVersion = "VERSION"
	tokenLinks   = "LINKS"
	tokenRecord  = "RECORD"
	tokenOwner   = "OWNER"

	linkValueKey = "PROPERTIES"

	currentPrefix      = tokenValue + delimiter
	recordPrefix       = tokenRecord + delimiter
	historyPrefixToken = tokenHistory + delimiter

	historyWindow = 50

	MaxValueBytes = 4096

	versionDigits = 10

	IndexName = "gsi1"
)

type Coordinate struct {
	Slug        string
	Folder      string
	Key         string
	Environment string

	Link string
}

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
	c.Environment = canonicalEnvironment(c.Environment)
	return c
}

func canonicalEnvironment(environment string) string {
	if environment == "" {
		return ClassWideEnvironment
	}
	return environment
}

func (c Coordinate) validate() error {
	if c.Slug == "" {
		return fmt.Errorf("a project slug is required")
	}
	if c.Link != "" {
		return fmt.Errorf("that value belongs to link %s: ocel derives it from the resource and prunes it, so there is nothing here to edit", c.Link)
	}
	if c.Key == "" {
		return fmt.Errorf("a variable name is required")
	}
	if c.Environment == ClassWideEnvironment {
		return fmt.Errorf("%q is reserved: it names the value that binds class-wide", ClassWideEnvironment)
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

func PartitionKey(slug, class string) string {
	return naming.VarsKey(slug, class)
}

func parsePartitionKey(pk string) (string, error) {
	slug, err := naming.ProjectOf(pk)
	if err != nil {
		return "", fmt.Errorf("unrecognized partition key %q", pk)
	}
	class, scoped := strings.CutPrefix(pk, naming.VarsKey(slug, ""))
	if !scoped || class == "" || strings.Contains(class, delimiter) {
		return "", fmt.Errorf("unrecognized partition key %q", pk)
	}
	return slug, nil
}

const linkIndexPrefix = tokenLinks + delimiter

func linkIndexSortKey(owner, environment string) string {
	return linkIndexPrefix + tokenOwner + delimiter + owner + delimiter + tokenEnv + delimiter + canonicalEnvironment(environment)
}

func parseLinkIndexSortKey(sk string) (owner, environment string, ok bool) {
	parts := strings.Split(sk, delimiter)
	if len(parts) != 5 || parts[0] != tokenLinks || parts[1] != tokenOwner || parts[3] != tokenEnv {
		return "", "", false
	}
	return parts[2], parts[4], true
}

func recordSortKey(environment string) string {
	return recordPrefix + tokenEnv + delimiter + canonicalEnvironment(environment)
}

func folderPrefix(folder string) string {
	return tokenFolder + delimiter + folder + delimiter
}

func keyPrefix(c Coordinate) string {
	return currentPrefix + folderPrefix(c.Folder) + tokenName + delimiter + c.Key + delimiter
}

func cellSuffix(c Coordinate) string {
	return folderPrefix(c.Folder) + tokenName + delimiter + c.Key + delimiter + tokenEnv + delimiter + c.Environment
}

func currentSortKey(c Coordinate) string {
	return currentPrefix + cellSuffix(c)
}

func historySortKey(c Coordinate, version int64) string {
	return historyPrefix(c) + fmt.Sprintf("%0*d", versionDigits, version)
}

func historyPrefix(c Coordinate) string {
	return historyPrefixToken + cellSuffix(c) + delimiter + tokenVersion + delimiter
}

func parseCurrentSortKey(slug, sk string) (Coordinate, error) {
	parts := strings.Split(sk, delimiter)
	if len(parts) != 7 || parts[0] != tokenValue || parts[1] != tokenFolder || parts[3] != tokenName || parts[5] != tokenEnv {
		return Coordinate{}, fmt.Errorf("unrecognized value key %q", sk)
	}
	c := Coordinate{Slug: slug, Folder: parts[2], Key: parts[4], Environment: parts[6]}
	if c.Folder == rootFolder {
		c.Folder = ""
	}
	if c.Environment == ClassWideEnvironment {
		c.Environment = ""
	}
	return c, nil
}

func parseHistorySortKey(sk string) (int64, error) {
	parts := strings.Split(sk, delimiter)
	if len(parts) != 9 || parts[0] != tokenHistory || parts[7] != tokenVersion {
		return 0, fmt.Errorf("unrecognized version key %q", sk)
	}
	return strconv.ParseInt(parts[8], 10, 64)
}
