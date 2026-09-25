package switchboard

import "net/http"

const (
	AdmitPath  = "/admit"
	AdmitField = "domain"
)

func (b *Board) admit() http.Handler {
	admit := http.NewServeMux()
	admit.HandleFunc("GET "+AdmitPath, b.serveAdmit)
	return admit
}

func (b *Board) serveAdmit(w http.ResponseWriter, r *http.Request) {
	if r.Context().Value(frontKey{}) == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	named := r.URL.Query()[AdmitField]
	if len(named) != 1 || !b.table.Load().Admits(named[0]) {
		w.WriteHeader(http.StatusForbidden)
	}
}
