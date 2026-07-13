package deploymentsv1

import "fmt"

// CheckClass returns nil when infra, the class the pointed-at infrastructure is
// stamped with, matches required, the class the running command demands (`ocel
// preview` requires CLASS_PREVIEW; `ocel deploy` requires CLASS_PRODUCTION).
// Otherwise it returns a concrete, directive error naming the real command and
// infrastructure at fault. It is shared by the CLI preflight and the provider
// so both refuse a class mismatch identically.
func CheckClass(infra, required Environment_Class) error {
	if infra == required {
		return nil
	}

	switch required {
	case Environment_CLASS_PREVIEW:
		return fmt.Errorf(
			"ocel preview can only run against preview infrastructure, but the account points at %s; run `ocel bootstrap --preview` to stand up preview infrastructure",
			infraLabel(infra),
		)
	case Environment_CLASS_PRODUCTION:
		return fmt.Errorf(
			"ocel deploy can only run against production infrastructure, but the account points at %s; run `ocel bootstrap` to stand up production infrastructure",
			infraLabel(infra),
		)
	default:
		return fmt.Errorf(
			"the account points at %s, but this command requires %s",
			infraLabel(infra), infraLabel(required),
		)
	}
}

// infraLabel renders an infrastructure class as concrete, user-facing prose,
// naming an unstamped account plainly rather than as an enum token.
func infraLabel(class Environment_Class) string {
	switch class {
	case Environment_CLASS_DEVELOPMENT:
		return "development infrastructure"
	case Environment_CLASS_PREVIEW:
		return "preview infrastructure"
	case Environment_CLASS_PRODUCTION:
		return "production infrastructure"
	default:
		return "no Ocel infrastructure"
	}
}
