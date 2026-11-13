package pulumi

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/pricing/ruleset"
)

func TestTheBridgedNamesCoverEveryPricedType(t *testing.T) {
	covered := append(slices.Collect(maps.Keys(bridged)), unbridged...)
	slices.Sort(covered)
	if priced := slices.Sorted(maps.Keys(ruleset.Table())); !slices.Equal(covered, priced) {
		t.Errorf("bridged.go covers %v, the rules price %v; run go generate ./pkg/pricing/source/pulumi", covered, priced)
	}
}

func TestNoTokenTheBridgeMapsIsAlsoAHandException(t *testing.T) {
	for token := range bridgedTokens {
		if vendor, typ, ok := terraformType(token); !ok || bridged[typ].token != token {
			t.Errorf("%s = (%q, %q, %v), want the bridged type", token, vendor, typ, ok)
		}
		if module := token[:strings.LastIndex(token, ":")]; renamed[module] != "" {
			t.Errorf("%s is both bridged and a hand exception", token)
		}
	}
}
