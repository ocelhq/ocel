package payloads

import (
	"strings"
	"testing"
)

func TestTheNodeMembraneIsCarriedAsOneBundle(t *testing.T) {
	body := NodeMembrane()
	if len(body) == 0 {
		t.Fatal("NodeMembrane() carries no bytes, and a node function boots through what it carries")
	}
	for _, relative := range []string{`from "./`, `from "../`} {
		if strings.Contains(string(body), relative) {
			t.Errorf("NodeMembrane() reads %s, and the image carries this file alone", relative)
		}
	}
}
