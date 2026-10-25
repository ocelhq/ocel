package providerserver

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/bindings"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type boundVariable struct {
	app      string
	variable *contractv1.ManifestVariable
}

func (b boundVariable) spelled() string {
	return fmt.Sprintf("%q", "@"+b.variable.GetBinding()+"."+b.variable.GetProperty())
}

func (r *deployRun) boundVariables() []boundVariable {
	var out []boundVariable
	for _, app := range r.manifest.GetApps() {
		for _, variable := range app.GetVariables() {
			if variable.GetBinding() != "" {
				out = append(out, boundVariable{app: app.GetName(), variable: variable})
			}
		}
	}
	return out
}

func unpublishedVariableRecords(bound []boundVariable, published map[string]bool) []string {
	var missing []string
	for _, b := range bound {
		if name := b.variable.GetBinding(); !published[name] && !slices.Contains(missing, name) {
			missing = append(missing, name)
		}
	}
	return missing
}

func (r *deployRun) refuseMismatchedVariables(ctx context.Context, bound []boundVariable) error {
	for _, b := range bound {
		if _, err := r.boundText(ctx, b); err != nil {
			return err
		}
	}
	return nil
}

func (r *deployRun) boundText(ctx context.Context, b boundVariable) (string, error) {
	name, property, key := b.variable.GetBinding(), b.variable.GetProperty(), b.variable.GetKey()
	published, found, err := r.publishedBindings().Record(ctx, name)
	if err != nil {
		return "", err
	}
	if !found {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"`bindings.variables` binds %s to %s, and nothing has published a record under %q. Run your infrastructure tool, then deploy again",
			key, b.spelled(), name)
	}
	record := published.GetBinding()
	if kind := bindings.TypeOf(record); kind != bindingsv1.BindingType_BINDING_TYPE_CUSTOM {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"`bindings.variables` binds %s to %s, and %s is a %s record, not a custom one: a variable takes a custom record's property. Publish the value in a custom binding",
			key, b.spelled(), name, strings.ToLower(bindings.EnvFragment(kind)))
	}
	text, ok := bindings.VariableText(record, property)
	if !ok {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"`bindings.variables` binds %s to %s, and %s publishes no %s: it publishes %s. Name one of those, or publish %s",
			key, b.spelled(), name, property, strings.Join(bindings.PropertyNames(record), ", "), property)
	}
	declared := b.variable.GetClass()
	secretVariable := declared == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET
	switch secretProperty := slices.Contains(published.GetSecrets(), property); {
	case secretProperty && !secretVariable:
		return "", refusal.Refuse(refusal.CodeInvalid,
			"app %s declares %s as %s, and %s is published as a secret: a secret reaches only a `secret` variable, which the runtime reads live and no release keeps. Declare %s as secret",
			b.app, key, className(declared), b.spelled(), key)
	case !secretProperty && secretVariable:
		return "", refusal.Refuse(refusal.CodeInvalid,
			"app %s declares %s as secret, and %s is published as a plain value: a secret variable takes only a property its publisher marked secret. Publish it as a secret, or declare %s as plain or sensitive",
			b.app, key, b.spelled(), key)
	}
	return text, nil
}

func className(class resourcesv1.VariableClass) string {
	return strings.ToLower(strings.TrimPrefix(class.String(), "VARIABLE_CLASS_"))
}
