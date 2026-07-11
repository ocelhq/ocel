// Package classguard makes it structurally impossible for a preview command to
// act on production infrastructure, or a deploy to act on preview
// infrastructure. It compares the class tag stamped on the targeted
// infrastructure against the class the running command requires.
package classguard

import "fmt"

// Class tags stamped on infrastructure at bootstrap and required by commands.
const (
	ClassDevelopment = "development"
	ClassPreview     = "preview"
	ClassProduction  = "production"
)

// Check returns nil when infraClass matches requiredClass — the class the
// running command demands (ocel preview requires "preview"; ocel deploy
// requires "production"). Otherwise it returns a concrete, directive error
// that names the real command and infrastructure at fault.
func Check(infraClass, requiredClass string) error {
	if infraClass == requiredClass {
		return nil
	}

	switch requiredClass {
	case ClassPreview:
		return fmt.Errorf(
			"ocel preview can only run against preview infrastructure, but the targeted infrastructure is tagged %q; run `ocel bootstrap --preview` to stand up preview infrastructure",
			infraTag(infraClass),
		)
	case ClassProduction:
		return fmt.Errorf(
			"ocel deploy can only run against production infrastructure, but the targeted infrastructure is tagged %q; run `ocel bootstrap` to stand up production infrastructure",
			infraTag(infraClass),
		)
	default:
		return fmt.Errorf(
			"infrastructure is tagged %q, but this command requires %q infrastructure",
			infraTag(infraClass), requiredClass,
		)
	}
}

// infraTag renders an infrastructure class tag for an error message, naming an
// untagged infrastructure concretely rather than printing an empty string.
func infraTag(class string) string {
	if class == "" {
		return "untagged"
	}
	return class
}
