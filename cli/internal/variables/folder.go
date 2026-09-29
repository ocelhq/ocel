package variables

import (
	"fmt"
	"strings"
)

const keyDelimiter = "#"

func ValidateFolder(folder string) error {
	switch {
	case folder == "":
		return fmt.Errorf("a folder is required")
	case !strings.HasPrefix(folder, "/"):
		return fmt.Errorf("folder %q must start with %q", folder, "/")
	case folder == "/":
		return fmt.Errorf("folder %q is the project root, which is what an unbound app already reads; leave the folder off instead", folder)
	case strings.HasSuffix(folder, "/"):
		return fmt.Errorf("folder %q must not end with %q", folder, "/")
	case strings.Contains(folder, "//"):
		return fmt.Errorf("folder %q has an empty path segment", folder)
	case strings.Contains(folder, keyDelimiter):
		return fmt.Errorf("folder %q may not contain %q", folder, keyDelimiter)
	}
	return nil
}
