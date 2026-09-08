package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"example.com/web/infra"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3104"
	}

	http.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "database": infra.DB.Name()})
	})

	log.Printf("go listening on http://localhost:%s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}
