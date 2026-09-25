package switchboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	LoadPath      = "/load"
	FlipPath      = "/flip"
	IdlePath      = "/idle"
	UpstreamsPath = "/upstreams"
)

const (
	TableField  = "table"
	RetireField = "retire"
	WindowField = "window"
	TargetField = "target"
)

func (b *Board) Control() http.Handler {
	control := http.NewServeMux()
	control.HandleFunc("POST "+LoadPath, b.serveLoad)
	control.HandleFunc("POST "+FlipPath, b.serveFlip)
	control.HandleFunc("POST "+IdlePath, b.serveIdle)
	control.HandleFunc("GET "+UpstreamsPath, b.serveUpstreams)
	return control
}

func unprocessable(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusUnprocessableEntity)
}

func (b *Board) serveLoad(w http.ResponseWriter, r *http.Request) {
	if err := b.Load(r.PostFormValue(TableField)); err != nil {
		unprocessable(w, err)
	}
}

func (b *Board) serveFlip(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		unprocessable(w, err)
		return
	}
	retiring := r.PostForm[RetireField]
	var window time.Duration
	if len(retiring) > 0 {
		read, err := time.ParseDuration(r.PostFormValue(WindowField))
		if err != nil || read <= 0 {
			unprocessable(w, fmt.Errorf("retiring %v needs a drain window, and %q is none", retiring, r.PostFormValue(WindowField)))
			return
		}
		window = read
	}
	told := false
	answer := http.NewResponseController(w)
	err := b.Flip(r.Context(), r.PostFormValue(TableField), retiring, window, func(drain Drain) {
		told = true
		fmt.Fprintln(w, drain)
		_ = answer.Flush()
	})
	switch {
	case err == nil:
	case told:
		panic(http.ErrAbortHandler)
	default:
		unprocessable(w, err)
	}
}

func (b *Board) serveIdle(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		unprocessable(w, err)
		return
	}
	idle, err := b.Idle(r.PostForm[TargetField])
	if err != nil {
		unprocessable(w, err)
		return
	}
	for _, target := range idle {
		fmt.Fprintln(w, target)
	}
}

func (b *Board) serveUpstreams(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(b.Upstreams())
}
