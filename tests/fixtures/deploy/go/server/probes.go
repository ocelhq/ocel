package main

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	maxSleepMs    = 60_000
	maxBody       = 8 << 20
	streamChunks  = 5
	streamGapMs   = 200
	streamEnd     = "ocel-stream-end"
	chunkBytes    = 64 << 10
	maxCookies    = 10
	maxEvents     = 10
	maxGapMs      = 60_000
	echoPrefix    = "/api/probes/echo/"
	probeHeader   = "x-ocel-probe"
	checksumField = "x-ocel-sha256"
)

var acceptsGzip = regexp.MustCompile(`\bgzip\b`)

type compressible struct {
	Marker string `json:"marker"`
	Filler string `json:"filler"`
}

type upload struct {
	Field  string `json:"field"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

func probes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/probes/stream", stream)
	mux.HandleFunc("GET /api/probes/sse", sse)
	mux.HandleFunc("GET /api/probes/status/{code}", status)
	mux.HandleFunc("GET /api/probes/empty/{kind}", empty)
	mux.HandleFunc("/api/probes/echo", echo)
	mux.HandleFunc(echoPrefix, echo)
	mux.HandleFunc("GET /api/probes/headers", headers)
	mux.HandleFunc("GET /api/probes/auth", auth)
	mux.HandleFunc("/api/probes/method", method)
	mux.HandleFunc("/api/probes/cors", cors)
	mux.HandleFunc("GET /api/probes/cookies", cookies)
	mux.HandleFunc("GET /api/probes/compress", compress)
	mux.HandleFunc("POST /api/probes/inflate", inflate)
	mux.HandleFunc("POST /api/probes/multipart", multipartForm)
	mux.HandleFunc("POST /api/probes/large", takeLarge)
	mux.HandleFunc("GET /api/probes/large", sendLarge)
	mux.HandleFunc("GET /api/probes/sleep", sleep)
}

func checksum(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func bounded(r *http.Request, name string, fallback, ceiling int) (int, bool) {
	query := r.URL.Query()
	if !query.Has(name) {
		return fallback, true
	}
	value, err := strconv.Atoi(query.Get(name))
	return value, err == nil && value >= 0 && value <= ceiling
}

func wait(r *http.Request, ms int) bool {
	select {
	case <-time.After(time.Duration(ms) * time.Millisecond):
		return true
	case <-r.Context().Done():
		return false
	}
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
		if !wait(r, streamGapMs) {
			return
		}
	}
	fmt.Fprintln(w, streamEnd)
}

func sse(w http.ResponseWriter, r *http.Request) {
	events, eventsOK := bounded(r, "events", 3, maxEvents)
	gap, gapOK := bounded(r, "gap", 1000, maxGapMs)
	if !eventsOK || !gapOK {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("events must be 0..%d and gap 0..%d", maxEvents, maxGapMs),
		})
		return
	}
	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-store, no-transform")
	w.WriteHeader(http.StatusOK)

	flush := http.NewResponseController(w)
	if err := flush.Flush(); err != nil {
		return
	}
	for i := 1; i <= events; i++ {
		fmt.Fprintf(w, "id: %d\ndata: ocel-sse-%d %d\n\n", i, i, time.Now().UnixMilli())
		if err := flush.Flush(); err != nil {
			return
		}
		if i < events && !wait(r, gap) {
			return
		}
	}
	fmt.Fprint(w, "event: end\ndata: ocel-sse-end\n\n")
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

func empty(w http.ResponseWriter, r *http.Request) {
	switch r.PathValue("kind") {
	case "redirect":
		w.Header().Set("location", "/api/probes/status/204")
		w.Header().Set("content-length", "0")
		w.WriteHeader(http.StatusFound)
	case "ok":
		w.Header().Set("content-length", "0")
		w.WriteHeader(http.StatusOK)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": fmt.Sprintf("no empty probe called %s", r.PathValue("kind")),
		})
	}
}

func echo(w http.ResponseWriter, r *http.Request) {
	read, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
		return
	}

	path, rawQuery, hasQuery := strings.Cut(r.RequestURI, "?")
	search := ""
	if hasQuery {
		search = "?" + rawQuery
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
		"method":   r.Method,
		"path":     path,
		"search":   search,
		"segments": segmentsOf(path),
		"query":    query,
		"header":   header,
		"body":     echoedBody(r, read),
	})
}

func segmentsOf(path string) []string {
	rest, found := strings.CutPrefix(path, echoPrefix)
	if !found || rest == "" {
		return []string{}
	}
	segments := strings.Split(rest, "/")
	for i, segment := range segments {
		if decoded, err := url.PathUnescape(segment); err == nil {
			segments[i] = decoded
		}
	}
	return segments
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

func headers(w http.ResponseWriter, r *http.Request) {
	seen := map[string]string{"host": r.Host}
	for name, values := range r.Header {
		seen[strings.ToLower(name)] = strings.Join(values, ", ")
	}

	var remote any
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		remote = host
	}

	protocol := "http"
	if r.TLS != nil {
		protocol = "https"
	}

	hostname := r.Host
	if host, _, err := net.SplitHostPort(r.Host); err == nil {
		hostname = host
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"headers":  seen,
		"remote":   remote,
		"ip":       remote,
		"protocol": protocol,
		"hostname": hostname,
	})
}

func auth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("www-authenticate", `Bearer realm="ocel"`)
	w.Header().Set("x-ocel-number", "42")
	writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
}

func method(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("allow", "GET, HEAD")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"error": fmt.Sprintf("%s is not allowed", r.Method),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"method": r.Method})
}

func cors(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("access-control-allow-origin", "*")
	w.Header().Set("access-control-allow-methods", "GET, POST, OPTIONS")
	w.Header().Set("access-control-allow-headers", "content-type, x-ocel-probe")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"method": r.Method})
}

func cookies(w http.ResponseWriter, r *http.Request) {
	count, ok := bounded(r, "count", 1, maxCookies)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("count must be an integer between 0 and %d", maxCookies),
		})
		return
	}
	for i := 1; i <= count; i++ {
		w.Header().Add("set-cookie", fmt.Sprintf("ocel-cookie-%d=value-%d; Path=/; HttpOnly", i, i))
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": count})
}

func compress(w http.ResponseWriter, r *http.Request) {
	body, err := json.Marshal(compressible{
		Marker: "ocel-compress",
		Filler: strings.Repeat("ocel ", 1024),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal error"})
		return
	}
	w.Header().Set("content-type", "application/json")
	w.Header().Set("vary", "accept-encoding")
	w.Header().Set(checksumField, checksum(body))
	if acceptsGzip.MatchString(r.Header.Get("accept-encoding")) {
		var zipped bytes.Buffer
		gz := gzip.NewWriter(&zipped)
		if _, err := gz.Write(body); err != nil || gz.Close() != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal error"})
			return
		}
		body = zipped.Bytes()
		w.Header().Set("content-encoding", "gzip")
	}
	w.Header().Set("content-length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func inflate(w http.ResponseWriter, r *http.Request) {
	var body io.Reader = http.MaxBytesReader(w, r.Body, maxBody)
	encoding := r.Header.Get("content-encoding")
	if strings.EqualFold(encoding, "gzip") {
		unzipped, err := gzip.NewReader(body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad gzip body"})
			return
		}
		body = io.LimitReader(unzipped, maxBody+1)
	}
	read, err := io.ReadAll(body)
	if err != nil || len(read) > maxBody {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
		return
	}

	var named any
	if encoding != "" {
		named = encoding
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"encoding": named,
		"bytes":    len(read),
		"sha256":   checksum(read),
	})
}

func multipartForm(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("content-type"), "multipart/form-data") {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"error": "multipart/form-data only"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	parts, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad multipart body"})
		return
	}

	fields := map[string]string{}
	files := []upload{}
	for {
		part, err := parts.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad multipart body"})
			return
		}
		read, err := io.ReadAll(part)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad multipart body"})
			return
		}
		if part.FileName() == "" {
			fields[part.FormName()] = string(read)
			continue
		}
		files = append(files, upload{
			Field:  part.FormName(),
			Name:   part.FileName(),
			Type:   part.Header.Get("content-type"),
			Bytes:  len(read),
			SHA256: checksum(read),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"fields": fields, "files": files})
}

func takeLarge(w http.ResponseWriter, r *http.Request) {
	read, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bytes":  len(read),
		"sha256": checksum(read),
	})
}

func sendLarge(w http.ResponseWriter, r *http.Request) {
	size, ok := bounded(r, "bytes", 0, maxBody)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("bytes must be an integer between 0 and %d", maxBody),
		})
		return
	}
	body := make([]byte, size)
	if _, err := rand.Read(body); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal error"})
		return
	}
	w.Header().Set("content-type", "application/octet-stream")
	w.Header().Set(checksumField, checksum(body))
	if !r.URL.Query().Has("chunked") {
		w.Header().Set("content-length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}

	w.WriteHeader(http.StatusOK)
	flush := http.NewResponseController(w)
	if err := flush.Flush(); err != nil {
		return
	}
	for offset := 0; offset < len(body); offset += chunkBytes {
		if _, err := w.Write(body[offset:min(offset+chunkBytes, len(body))]); err != nil {
			return
		}
		if err := flush.Flush(); err != nil {
			return
		}
	}
}

func sleep(w http.ResponseWriter, r *http.Request) {
	ms, ok := bounded(r, "ms", 0, maxSleepMs)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("ms must be an integer between 0 and %d", maxSleepMs),
		})
		return
	}
	if !wait(r, ms) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"slept": ms})
}
