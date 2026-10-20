package naming

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
)

const productionWord = "prod"

func TierWord(tier environment.Tier) string {
	if tier == environment.TierProduction {
		return productionWord
	}
	return Sanitize(string(tier))
}

func OcelName(namespace string, tier environment.Tier, words ...string) string {
	return NamespaceField(namespace) + WordSeparator + Join(WordSeparator, append([]string{TierWord(tier)}, words...)...)
}

func MustUntieredOcelName(namespace string, words ...string) string {
	rest := Join(WordSeparator, words...)
	if rest == "" {
		panic(fmt.Sprintf("an Ocel-owned name in namespace %q needs a word after the namespace", namespace))
	}
	if first, _, _ := strings.Cut(rest, WordSeparator); first == AppWord {
		panic(fmt.Sprintf("the Ocel-owned name %q starts with the fence %q that only app resources are named under", NamespaceField(namespace)+WordSeparator+rest, AppFence(namespace)))
	}
	return NamespaceField(namespace) + WordSeparator + rest
}
