package variables

import resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"

type Variable struct {
	Key              string
	Class            resourcesv1.VariableClass
	Value            string
	Folder           string
	ClientAccessible bool
	Version          int64
	Source           string
	SchemaSource     string
	Schema           bool
	Description      string
}
