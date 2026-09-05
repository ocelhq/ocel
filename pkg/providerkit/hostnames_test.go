package providerkit_test

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestAttributeHostnames(t *testing.T) {
	t.Parallel()

	t.Run("gives a project-level hostname to the first app alone", func(t *testing.T) {
		t.Parallel()

		served := providerkit.AttributeHostnames([]string{"shop.example"}, [][]string{nil, nil})
		if want := []string{"shop.example"}; !slices.Equal(served[0], want) {
			t.Errorf("the first app is served %v, want %v", served[0], want)
		}
		if len(served[1]) != 0 {
			t.Errorf("the second app is served %v, want nothing: only one app can answer for a project-level hostname", served[1])
		}
	})

	t.Run("puts an app's own hostnames ahead of the project's", func(t *testing.T) {
		t.Parallel()

		served := providerkit.AttributeHostnames(
			[]string{"shop.example", "www.shop.example"},
			[][]string{{"web.shop.example"}, {"admin.shop.example"}},
		)
		if want := []string{"web.shop.example", "shop.example", "www.shop.example"}; !slices.Equal(served[0], want) {
			t.Errorf("the first app is served %v, want %v: the url an app is told about is one it declares", served[0], want)
		}
		if want := []string{"admin.shop.example"}; !slices.Equal(served[1], want) {
			t.Errorf("the second app is served %v, want %v", served[1], want)
		}
	})

	t.Run("hands a hostname declared twice to the app that reaches it first", func(t *testing.T) {
		t.Parallel()

		served := providerkit.AttributeHostnames([]string{"shop.example"}, [][]string{nil, {"shop.example"}})
		if want := []string{"shop.example"}; !slices.Equal(served[0], want) {
			t.Errorf("the first app is served %v, want %v", served[0], want)
		}
		if len(served[1]) != 0 {
			t.Errorf("the second app is served %v, want nothing: two apps cannot both answer for one hostname", served[1])
		}
	})
}
