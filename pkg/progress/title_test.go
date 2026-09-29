package progress

import "testing"

func TestAVerbTitlesItsObjectAsItStartsAndAsItEnds(t *testing.T) {
	t.Parallel()

	got := Reading.Title("the routes for shop")
	want := Title{Started: "Reading the routes for shop", Ended: "Read the routes for shop"}
	if got != want {
		t.Errorf("Reading.Title() = %+v, want %+v", got, want)
	}
}
