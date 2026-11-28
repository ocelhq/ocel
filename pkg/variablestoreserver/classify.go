package variablestoreserver

import (
	"context"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func (h *Service) ClassifyValues(ctx context.Context, req *variablestorev1.ClassifyValuesRequest) (*variablestorev1.ClassifyValuesResponse, error) {
	store, scope, err := h.scoped(ctx, req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	declared := make(map[variablestore.Cell]variablestore.Class, len(req.GetCells()))
	for _, c := range req.GetCells() {
		declared[variablestore.Cell{Folder: c.GetFolder(), Key: c.GetKey()}] = classOf(c.GetClass())
	}
	raised, err := store.Classify(ctx, scope, declared)
	if err != nil {
		return nil, valuesError(err)
	}
	resp := &variablestorev1.ClassifyValuesResponse{Raised: make([]*variablestorev1.ValueMetadata, 0, len(raised))}
	for _, m := range raised {
		resp.Raised = append(resp.Raised, metadataProto(scope, m))
	}
	return resp, nil
}

func classOf(class resourcesv1.VariableClass) variablestore.Class {
	switch class {
	case resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN:
		return variablestore.ClassPlain
	case resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE:
		return variablestore.ClassSensitive
	case resourcesv1.VariableClass_VARIABLE_CLASS_SECRET:
		return variablestore.ClassSecret
	}
	return variablestore.ClassUnstated
}

func classProto(class variablestore.Class) resourcesv1.VariableClass {
	switch class {
	case variablestore.ClassPlain:
		return resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN
	case variablestore.ClassSensitive:
		return resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE
	case variablestore.ClassSecret:
		return resourcesv1.VariableClass_VARIABLE_CLASS_SECRET
	}
	return resourcesv1.VariableClass_VARIABLE_CLASS_UNSPECIFIED
}
