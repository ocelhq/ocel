package edge

import (
	"reflect"
	"testing"
)

func TestBoundDomains(t *testing.T) {
	t.Parallel()

	t.Run("a state nothing bound names no domain", func(t *testing.T) {
		t.Parallel()

		if got := BoundDomains(nil); got != nil {
			t.Errorf("BoundDomains(nil) = %v, want none", got)
		}
		if got := BoundDomains(StackState{StackKeySlug: "shop"}); got != nil {
			t.Errorf("BoundDomains = %v, want none", got)
		}
	})

	t.Run("recording a host twice binds it once", func(t *testing.T) {
		t.Parallel()

		state := RecordBoundDomain(RecordBoundDomain(nil, "shop.com"), "shop.com")
		if got := BoundDomains(state); !reflect.DeepEqual(got, []string{"shop.com"}) {
			t.Errorf("BoundDomains = %v, want [shop.com]", got)
		}
	})

	t.Run("hosts come back in a stable order", func(t *testing.T) {
		t.Parallel()

		state := RecordBoundDomain(RecordBoundDomain(nil, "shop.com"), "admin.shop.com")
		if got := BoundDomains(state); !reflect.DeepEqual(got, []string{"admin.shop.com", "shop.com"}) {
			t.Errorf("BoundDomains = %v, want both hosts sorted", got)
		}
	})

	t.Run("forgetting the last host leaves no key behind", func(t *testing.T) {
		t.Parallel()

		state := ForgetBoundDomain(RecordBoundDomain(StackState{StackKeySlug: "shop"}, "shop.com"), "shop.com")
		if _, present := state[StackKeyDomains]; present {
			t.Errorf("state = %v, want no domains key once nothing is bound", state)
		}
		if state[StackKeySlug] != "shop" {
			t.Errorf("state = %v, want the rest of the stack's state untouched", state)
		}
	})

	t.Run("recording and forgetting leave the state handed in untouched", func(t *testing.T) {
		t.Parallel()

		prior := StackState{StackKeySlug: "shop", StackKeyDomains: "shop.com"}

		if got := BoundDomains(RecordBoundDomain(prior, "admin.shop.com")); len(got) != 2 {
			t.Fatalf("BoundDomains = %v, want both hosts on the returned state", got)
		}
		if !reflect.DeepEqual(prior, StackState{StackKeySlug: "shop", StackKeyDomains: "shop.com"}) {
			t.Errorf("state handed in = %v, want it untouched by RecordBoundDomain", prior)
		}

		if got := BoundDomains(ForgetBoundDomain(prior, "shop.com")); len(got) != 0 {
			t.Fatalf("BoundDomains = %v, want none on the returned state", got)
		}
		if !reflect.DeepEqual(prior, StackState{StackKeySlug: "shop", StackKeyDomains: "shop.com"}) {
			t.Errorf("state handed in = %v, want it untouched by ForgetBoundDomain", prior)
		}
	})

	t.Run("forgetting a host nothing bound leaves the rest bound", func(t *testing.T) {
		t.Parallel()

		state := ForgetBoundDomain(RecordBoundDomain(nil, "shop.com"), "other.com")
		if got := BoundDomains(state); !reflect.DeepEqual(got, []string{"shop.com"}) {
			t.Errorf("BoundDomains = %v, want [shop.com]", got)
		}
	})
}
