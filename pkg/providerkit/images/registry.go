package images

import (
	"fmt"
	"strings"
)

type Registry struct {
	Server    string
	Namespace string
	Username  string
	Password  string
}

func (t Registry) String() string {
	return fmt.Sprintf("registry %s namespace %q username %q password [redacted]", t.Server, t.Namespace, t.Username)
}

func (t Registry) GoString() string { return t.String() }

func (t Registry) Named() bool { return t.Server != "" }

func (t Registry) ImageRef(repository, tag string) string {
	parts := []string{t.Server}
	if t.Namespace != "" {
		parts = append(parts, strings.Trim(t.Namespace, "/"))
	}
	return strings.Join(append(parts, repository), "/") + ":" + tag
}
