package vars

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	delimiter = "#"

	classWideEnvironment = "*"

	rootFolder = "/"

	currentPrefix      = "V" + delimiter
	historyPrefixToken = "H" + delimiter

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
	if c.Environment == "" {
		c.Environment = classWideEnvironment
	}
	return c
}

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

func PartitionKey(slug, class string) string {
	return "P" + delimiter + slug + delimiter + "C" + delimiter + class
}

func parsePartitionKey(pk string) (string, error) {
	parts := strings.Split(pk, delimiter)
	if len(parts) != 4 || parts[0] != "P" || parts[2] != "C" {
		return "", fmt.Errorf("unrecognized partition key %q", pk)
	}
	return parts[1], nil
}

func folderPrefix(folder string) string {
	return "F" + delimiter + folder + delimiter
}

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

func parseHistorySortKey(sk string) (int64, error) {
	parts := strings.Split(sk, delimiter)
	if len(parts) != 9 || parts[0] != "H" || parts[7] != "N" {
		return 0, fmt.Errorf("unrecognized version key %q", sk)
	}
	return strconv.ParseInt(parts[8], 10, 64)
}
