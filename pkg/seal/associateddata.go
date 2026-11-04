package seal

import (
	"fmt"
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

func (a AssociatedData) Purpose() string { return a.purpose }

func (a AssociatedData) Values() []string { return slices.Clone(a.values) }

func ParseAssociatedData(bound []byte) (AssociatedData, error) {
	var elements []string
	for rest := string(bound); rest != ""; {
		length, after, found := strings.Cut(rest, ":")
		n, err := strconv.Atoi(length)
		if !found || err != nil || n < 0 || strconv.Itoa(n) != length || n >= len(after) || after[n] != ',' {
			return AssociatedData{}, fmt.Errorf("associated data %q is not a list of <length>:<element>, elements", bound)
		}
		elements, rest = append(elements, after[:n]), after[n+1:]
	}
	if len(elements) < 2 {
		return AssociatedData{}, fmt.Errorf("associated data %q names no purpose and project", bound)
	}
	return AssociatedData{purpose: elements[0], project: elements[1], values: elements[2:]}, nil
}
