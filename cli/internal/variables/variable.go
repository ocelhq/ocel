package variables

import resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"

type Variable struct {
	Key         string
	Class       resourcesv1.VariableClass
	Value       string
	Folder      string
	Version     int64
	Source      string
	Description string
}
