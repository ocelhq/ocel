package payloads

import (
	"strings"
	"testing"
)

func TestTheNodeRuntimeIsCarriedAsOneBundle(t *testing.T) {
	body := NodeRuntime()
	if len(body) == 0 {
		t.Fatal("NodeRuntime() carries no bytes, and a node function boots through what it carries")
	}
	for _, relative := range []string{`from "./`, `from "../`} {
		if strings.Contains(string(body), relative) {
			t.Errorf("NodeRuntime() reads %s, and the image carries this file alone", relative)
		}
	}
}
