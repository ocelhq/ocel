package kvstore

import (
	"fmt"
	"regexp"
)

const maxEntryNameBytes = 63

var entryNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

var reservedEntryNames = map[string]string{
	"client":            "every SDK, where it is the store's own Redis client",
	"connectionString":  "TypeScript, where it is the store's connection string",
	"connection_string": "Python and Rust, where it is the store's connection string",
	"then":              "TypeScript, where a member named then makes await read the store as a promise",
	"constructor":       "TypeScript, where every object already has one",
}

func RefuseEntryName(name string) error {
	if reserved, taken := reservedEntryNames[name]; taken {
		return fmt.Errorf("name %q is reserved by %s: name the entry otherwise", name, reserved)
	}
	if len(name) > maxEntryNameBytes || !entryNamePattern.MatchString(name) {
		return fmt.Errorf("name %q is no name every SDK can hold: an entry name starts with a letter and goes on in letters, digits and _, at most %d characters", name, maxEntryNameBytes)
	}
	return nil
}
