package ports

import "strings"

func (c Coordinate) Binding() []byte {
	var bound strings.Builder
	for _, part := range []string{c.Project, string(c.Class), c.Env, c.Folder, c.Link, c.Name} {
		bound.WriteString(Escape(part))
		bound.WriteByte('/')
	}
	return []byte(bound.String())
}

func Escape(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "%", "%25"), "/", "%2F")
}

func Unescape(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "%2F", "/"), "%25", "%")
}
