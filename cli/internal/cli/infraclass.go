package cli

import (
	"fmt"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func checkClass(infra, required deploymentsv1.Environment_Class) error {
	if infra == required {
		return nil
	}

	switch required {
	case deploymentsv1.Environment_CLASS_PREVIEW:
		return fmt.Errorf(
			"ocel preview can only run against preview infrastructure, but the account points at %s; run `ocel bootstrap --preview` to stand up preview infrastructure",
			infraLabel(infra),
		)
	case deploymentsv1.Environment_CLASS_PRODUCTION:
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

func infraLabel(class deploymentsv1.Environment_Class) string {
	switch class {
	case deploymentsv1.Environment_CLASS_PREVIEW:
		return "preview infrastructure"
	case deploymentsv1.Environment_CLASS_PRODUCTION:
		return "production infrastructure"
	default:
		return "no Ocel infrastructure"
	}
}
