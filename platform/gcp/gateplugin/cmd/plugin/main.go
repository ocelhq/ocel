package main

import (
	"time"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
)

func main() {
	g := previewgate.Gate{Key: "k"}
	v := g.Check(previewgate.Request{Method: "GET", Target: "/", Header: previewgate.Header{}}, time.Now())
	if v.Response != nil {
		println(v.Response.Body)
	}
}
