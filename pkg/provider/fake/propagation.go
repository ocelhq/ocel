package fake

import "github.com/ocelhq/ocel/pkg/router"

func (d *DataPlane) Propagates(propagation router.Propagation) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.propagates = &propagation
}

func (d *DataPlane) propagation() (router.Propagation, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.propagates == nil {
		return router.Propagation{}, false
	}
	return *d.propagates, true
}
