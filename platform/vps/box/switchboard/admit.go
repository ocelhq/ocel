package switchboard

import (
	"net/http"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

func (b *Board) admit() http.Handler {
	admit := http.NewServeMux()
	admit.HandleFunc("GET "+boxapi.AdmitPath, b.serveAdmit)
	return admit
}

func (b *Board) serveAdmit(w http.ResponseWriter, r *http.Request) {
	if arrivedOver(r) != overFront {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	named := r.URL.Query()[boxapi.AdmitField]
	if len(named) != 1 || !b.table.Load().Admits(named[0]) {
		w.WriteHeader(http.StatusForbidden)
	}
}
