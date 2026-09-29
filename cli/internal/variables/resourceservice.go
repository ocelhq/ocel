package variables

import (
	"context"
	"fmt"
	"slices"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"google.golang.org/protobuf/proto"
)

func (d *Declarations) DeclareEnv(ctx context.Context, req *resourcesv1.DeclareEnvRequest) (*resourcesv1.DeclareEnvResponse, error) {
	groups := map[string]bool{}
	for _, group := range req.GetGroups() {
		if group.GetKey() == "" {
			return nil, fmt.Errorf("an environment group needs a key")
		}
		if groups[group.GetKey()] {
			return nil, fmt.Errorf("environment group %s is declared twice", group.GetKey())
		}
		groups[group.GetKey()] = true
	}
	for _, definition := range req.GetDefinitions() {
		if definition.GetGroup() != "" && !groups[definition.GetGroup()] {
			return nil, fmt.Errorf("%s belongs to environment group %s, but that group is not declared", definition.GetKey(), definition.GetGroup())
		}
		if definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_DERIVED {
			return nil, fmt.Errorf("%s is declared as derived, a class ocel writes for the resources an app binds and prunes on its own; declare it as plain, sensitive or secret", definition.GetKey())
		}
		if d.scope.IsWrittenByOcel(definition.GetKey(), definition.GetFolders()) {
			return nil, fmt.Errorf("%s is written by ocel for the apps this declaration reaches, from the hostname this deploy serves it on, so a declared one would be overwritten before anything read it; read it from `ocel/env` as `deployment.url` instead of declaring it", definition.GetKey())
		}
	}
	for _, group := range req.GetGroups() {
		if !slices.ContainsFunc(req.GetDefinitions(), func(definition *resourcesv1.VariableDefinition) bool {
			return definition.GetGroup() == group.GetKey()
		}) {
			return nil, fmt.Errorf("environment group %s has no members", group.GetKey())
		}
	}
	if err := collision(req.GetDefinitions(), d.scope); err != nil {
		return nil, err
	}

	present, err := d.claim(req)
	if err != nil {
		return nil, err
	}

	var wanted []Cell
	for _, definition := range req.GetDefinitions() {
		if definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET {
			continue
		}
		wanted = append(wanted, cellsOf(present, definition.GetKey())...)
	}
	plaintext, err := d.reveal(ctx, wanted)
	if err != nil {
		return nil, err
	}

	var cells []*resourcesv1.VariableCell
	for _, definition := range req.GetDefinitions() {
		live := definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET
		for _, cell := range cellsOf(present, definition.GetKey()) {
			var value string
			if !live {
				if !plaintext[cell].found {
					continue
				}
				value = plaintext[cell].value
			}
			cells = append(cells, &resourcesv1.VariableCell{
				Key:    cell.Key,
				Folder: cell.Folder,
				Value:  value,
			})
		}
	}

	return &resourcesv1.DeclareEnvResponse{Cells: cells}, nil
}

func (d *Declarations) ReportEnvProblems(_ context.Context, req *resourcesv1.ReportEnvProblemsRequest) (*resourcesv1.ReportEnvProblemsResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, problem := range req.GetProblems() {
		if problem.GetDetail() != "" && !isPlain(d.definitions, problem.GetKey()) {
			problem = proto.CloneOf(problem)
			problem.Detail = ""
		}
		d.problems = append(d.problems, problem)
	}
	return &resourcesv1.ReportEnvProblemsResponse{}, nil
}

func isPlain(definitions []*resourcesv1.VariableDefinition, key string) bool {
	declared := false
	for _, definition := range definitions {
		if definition.GetKey() != key {
			continue
		}
		if definition.GetClass() != resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN {
			return false
		}
		declared = true
	}
	return declared
}
