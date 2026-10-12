package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

func writeAnswer(w http.ResponseWriter, status int, body any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3104"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeAnswer(w, http.StatusOK, map[string]any{"ok": true, "app": "web"})
	})
	handleWire(mux)
	handleRealtime(mux)

	log.Printf("go listening on http://localhost:%s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}
