package provider

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type Namespace string

const DefaultNamespace Namespace = "ocel"

const (
	MaxNamespaceLength = 44
	NamespaceEnvVar    = "OCEL_NAMESPACE"
)

func ParseNamespace(given string) (Namespace, error) {
	if given == "" {
		return DefaultNamespace, nil
	}
	if len(given) > MaxNamespaceLength {
		return "", fmt.Errorf("namespace %q is %d characters; every name a bootstrap derives from it has to fit the shortest limit it lands under, which leaves %d", given, len(given), MaxNamespaceLength)
	}
	if err := naming.Validate("namespace", given); err != nil || given[0] < 'a' || given[0] > 'z' {
		return "", fmt.Errorf("namespace %q is not the field every name derived from it contains: start with a lowercase letter, use only lowercase letters, digits and single dashes, and end with a letter or digit", given)
	}
	if slices.Contains(strings.Split(given, naming.WordSeparator), naming.AppWord) {
		return "", fmt.Errorf("namespace %q has the word %q between dashes; every app resource is named under the fence <namespace>-%s-, so another namespace's fence would cover its names", given, naming.AppWord, naming.AppWord)
	}
	if naming.IsCompactedNamespaceField(given) {
		return "", fmt.Errorf("namespace %q has the shape a namespace longer than %d characters is compacted to in names, so the two could share every name; pick another", given, naming.MaxNamespaceField)
	}
	return Namespace(given), nil
}

func NamespaceFromEnv() (Namespace, error) {
	ns, err := ParseNamespace(os.Getenv(NamespaceEnvVar))
	if err != nil {
		return "", refusal.Refuse(refusal.CodeInvalid, "%s: %s", NamespaceEnvVar, err.Error())
	}
	return ns, nil
}

func (n Namespace) String() string { return string(n) }
