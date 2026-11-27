package providerserver

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/router"
)

func recordReleasedVariables(record *router.ReleaseRecord, manifest *contractv1.Manifest, app *contractv1.ManifestApp) error {
	declarations, err := marshalEach(app.GetDeclarations())
	if err != nil {
		return fmt.Errorf("record the variables %s declares: %w", app.GetName(), err)
	}
	groups, err := marshalEach(manifest.GetVariableGroups())
	if err != nil {
		return fmt.Errorf("record the variable groups %s is deployed with: %w", app.GetName(), err)
	}
	record.Declarations, record.VariableGroups = declarations, groups
	for _, variable := range app.GetVariables() {
		if variable.GetVersion() <= 0 {
			continue
		}
		record.Values = append(record.Values, router.ReleasedValue{
			Key:         variable.GetKey(),
			Folder:      variable.GetFolder(),
			Environment: variable.GetEnvironment(),
			Version:     variable.GetVersion(),
			Class:       variable.GetClass().String(),
		})
	}
	return nil
}

func readReleasedVariables(result *progressv1.AppResult, record router.ReleaseRecord) error {
	declarations, err := unmarshalEach[resourcesv1.VariableDefinition](record.Declarations)
	if err != nil {
		return fmt.Errorf("read the variables release %s of %s declares: %w", record.Release, record.App, err)
	}
	groups, err := unmarshalEach[resourcesv1.GroupDefinition](record.VariableGroups)
	if err != nil {
		return fmt.Errorf("read the variable groups release %s of %s was deployed with: %w", record.Release, record.App, err)
	}
	result.Variables, result.VariableGroups = declarations, groups
	for _, value := range record.Values {
		result.Values = append(result.Values, &progressv1.ReleasedValue{
			Key:         value.Key,
			Folder:      value.Folder,
			Environment: value.Environment,
			Version:     value.Version,
			Class:       resourcesv1.VariableClass(resourcesv1.VariableClass_value[value.Class]),
		})
	}
	return nil
}

func marshalEach[M proto.Message](messages []M) ([]json.RawMessage, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	out := make([]json.RawMessage, 0, len(messages))
	for _, message := range messages {
		raw, err := protojson.Marshal(message)
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, nil
}

func unmarshalEach[T any, M interface {
	*T
	proto.Message
}](raws []json.RawMessage) ([]M, error) {
	if len(raws) == 0 {
		return nil, nil
	}
	out := make([]M, 0, len(raws))
	for _, raw := range raws {
		message := M(new(T))
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, message); err != nil {
			return nil, err
		}
		out = append(out, message)
	}
	return out, nil
}
