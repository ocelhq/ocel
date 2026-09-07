package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxSleep      = 30 * time.Second
	maxBody       = 8 << 20
	streamChunks  = 5
	streamGap     = 200 * time.Millisecond
	streamEnd     = "ocel-stream-end"
	probeHeader   = "x-ocel-probe"
	checksumField = "x-ocel-sha256"
)

func probes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/probes/stream", stream)
	mux.HandleFunc("GET /api/probes/status/{code}", status)
	mux.HandleFunc("/api/probes/echo", echo)
	mux.HandleFunc("/api/probes/echo/", echo)
	mux.HandleFunc("POST /api/probes/large", takeLarge)
	mux.HandleFunc("GET /api/probes/large", sendLarge)
	mux.HandleFunc("GET /api/probes/sleep", sleep)
}

func stream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("content-type", "text/plain; charset=utf-8")
	w.Header().Set("cache-control", "no-store, no-transform")
	w.WriteHeader(http.StatusOK)

	flush := http.NewResponseController(w)
	for i := 1; i < streamChunks; i++ {
		fmt.Fprintf(w, "ocel-stream-%d\n", i)
		if err := flush.Flush(); err != nil {
			return
		}
		select {
		case <-time.After(streamGap):
		case <-r.Context().Done():
			return
		}
	}
	fmt.Fprintln(w, streamEnd)
}

func status(w http.ResponseWriter, r *http.Request) {
	code, err := strconv.Atoi(r.PathValue("code"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "code must be a status"})
		return
	}
	if code >= 300 && code < 400 {
		w.Header().Set("location", "/api/probes/status/204")
	}
	if code == http.StatusNoContent {
		w.WriteHeader(code)
		return
	}
	writeJSON(w, code, map[string]any{"status": code})
}

func echo(w http.ResponseWriter, r *http.Request) {
	read, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
		return
	}

	query := map[string]string{}
	for key, values := range r.URL.Query() {
		query[key] = values[0]
	}

	var header any
	if named := r.Header.Get(probeHeader); named != "" {
		header = named
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"method": r.Method,
		"path":   r.URL.Path,
		"query":  query,
		"header": header,
		"body":   echoedBody(r, read),
	})
}

func echoedBody(r *http.Request, read []byte) any {
	if len(read) == 0 {
		return nil
	}
	if strings.Contains(r.Header.Get("content-type"), "application/json") {
		var decoded any
		if json.Unmarshal(read, &decoded) == nil {
			return decoded
		}
	}
	return string(read)
}

func takeLarge(w http.ResponseWriter, r *http.Request) {
	read, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
		return
	}
	sum := sha256.Sum256(read)
	writeJSON(w, http.StatusOK, map[string]any{
		"bytes":  len(read),
		"sha256": hex.EncodeToString(sum[:]),
	})
}

func sendLarge(w http.ResponseWriter, r *http.Request) {
	bytes, err := strconv.Atoi(numberOr(r, "bytes", "0"))
	if err != nil || bytes < 0 || bytes > maxBody {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("bytes must be an integer between 0 and %d", maxBody),
		})
		return
	}
	body := make([]byte, bytes)
	if _, err := rand.Read(body); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal error"})
		return
	}
	sum := sha256.Sum256(body)
	w.Header().Set("content-type", "application/octet-stream")
	w.Header().Set(checksumField, hex.EncodeToString(sum[:]))
	w.Header().Set("content-length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func sleep(w http.ResponseWriter, r *http.Request) {
	ms, err := strconv.Atoi(numberOr(r, "ms", "0"))
	if err != nil || ms < 0 || time.Duration(ms)*time.Millisecond > maxSleep {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("ms must be an integer between 0 and %d", maxSleep.Milliseconds()),
		})
		return
	}
	select {
	case <-time.After(time.Duration(ms) * time.Millisecond):
	case <-r.Context().Done():
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"slept": ms})
}

func numberOr(r *http.Request, name, fallback string) string {
	if named := r.URL.Query().Get(name); named != "" {
		return named
	}
	return fallback
}
