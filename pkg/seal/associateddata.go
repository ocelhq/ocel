package seal

import (
	"slices"
	"strconv"
	"strings"
)

type AssociatedData struct {
	purpose string
	project string
	values  []string
}

func Bind(purpose string, values ...string) AssociatedData {
	return AssociatedData{purpose: purpose, values: slices.Clone(values)}
}

func BindToProject(project, purpose string, values ...string) AssociatedData {
	return AssociatedData{purpose: purpose, project: project, values: slices.Clone(values)}
}

func (a AssociatedData) Project() (string, bool) {
	return a.project, a.project != ""
}

func (a AssociatedData) Bytes() []byte {
	var rendered strings.Builder
	for _, element := range append([]string{a.purpose, a.project}, a.values...) {
		rendered.WriteString(strconv.Itoa(len(element)))
		rendered.WriteByte(':')
		rendered.WriteString(element)
		rendered.WriteByte(',')
	}
	return []byte(rendered.String())
}
