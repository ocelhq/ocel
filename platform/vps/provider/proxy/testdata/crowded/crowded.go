package crowded

import "github.com/ocelhq/ocel/platform/vps/provider/proxy"

type Crowded struct {
	guarantees proxy.Guarantees
	admitted   proxy.Admission
}
