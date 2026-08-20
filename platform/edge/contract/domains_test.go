package edge

import (
	"reflect"
	"testing"
)

func TestBoundDomains(t *testing.T) {
	t.Parallel()

	t.Run("a state nothing bound names no domain", func(t *testing.T) {
		t.Parallel()

		if got := (StackState{}).Bound; got != nil {
			t.Errorf("bound domains = %v, want none", got)
		}
		if got := (StackState{Slug: "shop"}).Bound; got != nil {
			t.Errorf("bound domains = %v, want none", got)
		}
	})

	t.Run("recording a host twice binds it once", func(t *testing.T) {
		t.Parallel()

		var state StackState
		state.Bind("shop.com")
		state.Bind("shop.com")
		if !reflect.DeepEqual(state.Bound, []string{"shop.com"}) {
			t.Errorf("bound domains = %v, want [shop.com]", state.Bound)
		}
	})

	t.Run("hosts come back in a stable order", func(t *testing.T) {
		t.Parallel()

		var state StackState
		state.Bind("shop.com")
		state.Bind("admin.shop.com")
		if !reflect.DeepEqual(state.Bound, []string{"admin.shop.com", "shop.com"}) {
			t.Errorf("bound domains = %v, want both hosts sorted", state.Bound)
		}
	})

	t.Run("releasing the last host leaves nothing behind", func(t *testing.T) {
		t.Parallel()

		state := StackState{Slug: "shop"}
		state.Bind("shop.com")
		state.Release("shop.com")
		if state.Bound != nil {
			t.Errorf("bound domains = %v, want none once nothing is bound", state.Bound)
		}
		if state.Slug != "shop" {
			t.Errorf("slug = %q, want the rest of the stack's state untouched", state.Slug)
		}
	})

	t.Run("binding and releasing leave a state copied off untouched", func(t *testing.T) {
		t.Parallel()

		prior := StackState{Slug: "shop"}
		prior.Bind("shop.com")

		bound := prior
		bound.Bind("admin.shop.com")
		if len(bound.Bound) != 2 {
			t.Fatalf("bound domains = %v, want both hosts on the copy", bound.Bound)
		}
		if !reflect.DeepEqual(prior.Bound, []string{"shop.com"}) {
			t.Errorf("bound domains on the state copied off = %v, want binding not to reach back", prior.Bound)
		}

		released := prior
		released.Release("shop.com")
		if len(released.Bound) != 0 {
			t.Fatalf("bound domains = %v, want none on the copy", released.Bound)
		}
		if !reflect.DeepEqual(prior.Bound, []string{"shop.com"}) {
			t.Errorf("bound domains on the state copied off = %v, want releasing not to reach back", prior.Bound)
		}
	})

	t.Run("releasing a host nothing bound leaves the rest bound", func(t *testing.T) {
		t.Parallel()

		var state StackState
		state.Bind("shop.com")
		state.Release("other.com")
		if !reflect.DeepEqual(state.Bound, []string{"shop.com"}) {
			t.Errorf("bound domains = %v, want [shop.com]", state.Bound)
		}
	})

	t.Run("a host is reported bound only once it is", func(t *testing.T) {
		t.Parallel()

		var state StackState
		if state.BoundTo("shop.com") {
			t.Error("BoundTo = true on a state nothing bound")
		}
		state.Bind("shop.com")
		if !state.BoundTo("shop.com") {
			t.Error("BoundTo = false on the host just bound")
		}
	})
}
