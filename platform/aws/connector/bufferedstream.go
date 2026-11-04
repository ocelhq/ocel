package main

import "net/http"

type bufferedStream struct{ http.ResponseWriter }

func (bufferedStream) Flush() {}

func bufferingStreams(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, flushes := w.(http.Flusher); !flushes {
			w = bufferedStream{ResponseWriter: w}
		}
		handler.ServeHTTP(w, r)
	})
}
