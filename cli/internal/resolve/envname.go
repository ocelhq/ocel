package resolve

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func EnvName(t resourcesv1.ResourceType, name string) (string, error) {
	bound, bindable := naming.BindableAs(t)
	if !bindable {
		return "", fmt.Errorf("resource has unsupported type %s", t)
	}
	return naming.ResourceEnvName(bound, name), nil
}
