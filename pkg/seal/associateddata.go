package seal

import "strings"

type Field struct {
	Name  string
	Value string
}

type AssociatedData []Field

func (a AssociatedData) Bytes() []byte {
	var rendered strings.Builder
	for _, field := range a {
		rendered.WriteString(strings.ReplaceAll(strings.ReplaceAll(field.Value, "%", "%25"), "/", "%2F"))
		rendered.WriteByte('/')
	}
	return []byte(rendered.String())
}
