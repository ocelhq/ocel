package resolve

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/resourceregistry"
	"github.com/ocelhq/ocel/pkg/naming"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
)

type Missing struct {
	Resources []resourceregistry.Entry
}

func (m *Missing) Error() string {
	names := make([]string, 0, len(m.Resources))
	for _, resource := range m.Resources {
		names = append(names, fmt.Sprintf("%q", resource.Name))
	}
	return "no value for resource " + strings.Join(names, ", ")
}

func EnvFragment(t linksv1.LinkType) (string, error) {
	if _, ok := naming.KindOf(t); !ok {
		return "", fmt.Errorf("resource has unsupported type %s", t)
	}
	return naming.EnvFragment(t), nil
}

func EnvName(t linksv1.LinkType, name string) (string, error) {
	if _, err := EnvFragment(t); err != nil {
		return "", err
	}
	return naming.ResourceEnvName(t, name), nil
}

func FromEnv(resources []resourceregistry.Entry, env map[string]string) ([]Resource, error) {
	out := make([]Resource, 0, len(resources))
	var missing []resourceregistry.Entry
	for _, resource := range resources {
		key, err := EnvName(resource.Type, resource.Name)
		if err != nil {
			return nil, err
		}
		value, ok := env[key]
		if !ok {
			missing = append(missing, resource)
			continue
		}
		out = append(out, Resource{Name: resource.Name, Type: resource.Type, Env: map[string]string{key: value}})
	}
	if len(missing) > 0 {
		return nil, &Missing{Resources: missing}
	}
	return out, nil
}

func MissingResources(err error) []resourceregistry.Entry {
	var missing *Missing
	if errors.As(err, &missing) {
		return missing.Resources
	}
	return nil
}
