package bucket

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"io"
	"mime"
	mimeparts "mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	raw "google.golang.org/api/storage/v1"
)

type storedObject struct {
	body        []byte
	contentType string
	cache       string
	metadata    map[string]string
	generation  int64
	created     time.Time
}

type storageServer struct {
	mu         sync.Mutex
	objects    map[string]map[string]*storedObject
	generation int64
	uploads    map[string]multipartUpload
	nextUpload int

	writes     []url.Values
	completed  []string
	aborted    []string
	authorized []string
}

type multipartUpload struct {
	bucket, key, contentType string
	metadata                 map[string]string
}

func startStorage(t *testing.T) (*storageServer, string) {
	t.Helper()
	server := &storageServer{objects: map[string]map[string]*storedObject{}, uploads: map[string]multipartUpload{}}
	served := httptest.NewServer(server)
	t.Cleanup(served.Close)
	return server, served.URL
}

func (s *storageServer) put(bucket, key string, body []byte, contentType string) *storedObject {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store(bucket, key, body, contentType, nil)
}

func (s *storageServer) store(bucket, key string, body []byte, contentType string, metadata map[string]string) *storedObject {
	if s.objects[bucket] == nil {
		s.objects[bucket] = map[string]*storedObject{}
	}
	s.generation++
	object := &storedObject{body: body, contentType: contentType, metadata: metadata, generation: 1_700_000_000_000_000 + s.generation,
		created: time.Date(2026, 10, 5, 12, 0, int(s.generation), 0, time.UTC)}
	s.objects[bucket][key] = object
	return object
}

func (s *storageServer) object(bucket, key string) *storedObject {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objects[bucket][key]
}

func (s *storageServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authorized = append(s.authorized, r.Header.Get("Authorization"))
	path := r.URL.EscapedPath()
	switch {
	case strings.HasPrefix(path, "/upload/storage/v1/b/"):
		s.insert(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/upload/storage/v1/b/"), "/o"))
	case strings.HasPrefix(path, "/storage/v1/b/"):
		s.json(w, r, strings.Split(strings.TrimPrefix(path, "/storage/v1/b/"), "/"))
	default:
		s.xml(w, r, strings.TrimPrefix(path, "/"))
	}
}

func (s *storageServer) json(w http.ResponseWriter, r *http.Request, parts []string) {
	w.Header().Set("Content-Type", "application/json")
	bucket := parts[0]
	switch {
	case len(parts) == 2 && parts[1] == "o" && r.Method == http.MethodGet:
		s.list(w, r, bucket)
	case len(parts) == 3 && parts[1] == "o":
		key, _ := url.PathUnescape(parts[2])
		s.single(w, r, bucket, key)
	case len(parts) == 8 && parts[3] == "rewriteTo":
		source, _ := url.PathUnescape(parts[2])
		destination, _ := url.PathUnescape(parts[7])
		from := s.objects[bucket][source]
		if from == nil {
			notFound(w)
			return
		}
		copied := s.store(parts[5], destination, from.body, from.contentType, from.metadata)
		writeJSON(w, map[string]any{"kind": "storage#rewriteResponse", "done": true, "resource": s.resource(parts[5], destination, copied),
			"totalBytesRewritten": strconv.Itoa(len(from.body)), "objectSize": strconv.Itoa(len(from.body))})
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (s *storageServer) single(w http.ResponseWriter, r *http.Request, bucket, key string) {
	object := s.objects[bucket][key]
	if object == nil {
		notFound(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Get("alt") == "media" {
			w.Header().Set("Content-Type", object.contentType)
			w.Header().Set("X-Goog-Generation", strconv.FormatInt(object.generation, 10))
			w.Header().Set("X-Goog-Metageneration", "1")
			w.Header().Set("Content-Length", strconv.Itoa(len(object.body)))
			w.Write(object.body)
			return
		}
		writeJSON(w, s.resource(bucket, key, object))
	case http.MethodDelete:
		delete(s.objects[bucket], key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (s *storageServer) resource(bucket, key string, object *storedObject) *raw.Object {
	return &raw.Object{
		Kind: "storage#object", Bucket: bucket, Name: key, Size: uint64(len(object.body)),
		ContentType: object.contentType, CacheControl: object.cache, Metadata: object.metadata,
		Generation: object.generation, Metageneration: 1,
		TimeCreated: object.created.Format(time.RFC3339Nano), Updated: object.created.Add(time.Hour).Format(time.RFC3339Nano),
		Etag:   "CJ" + strconv.FormatInt(object.generation, 10),
		Crc32c: checksumOf(object.body),
	}
}

func (s *storageServer) list(w http.ResponseWriter, r *http.Request, bucket string) {
	query := r.URL.Query()
	var keys []string
	for key := range s.objects[bucket] {
		if strings.HasPrefix(key, query.Get("prefix")) && key > query.Get("pageToken") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	listed := &raw.Objects{Kind: "storage#objects"}
	limit, _ := strconv.Atoi(query.Get("maxResults"))
	for i, key := range keys {
		if limit > 0 && i == limit {
			listed.NextPageToken = keys[i-1]
			break
		}
		listed.Items = append(listed.Items, s.resource(bucket, key, s.objects[bucket][key]))
	}
	writeJSON(w, listed)
}

func (s *storageServer) insert(w http.ResponseWriter, r *http.Request, bucket string) {
	query := r.URL.Query()
	s.writes = append(s.writes, query)
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	parts := mimeparts.NewReader(r.Body, params["boundary"])
	head, _ := parts.NextPart()
	var described raw.Object
	_ = json.NewDecoder(head).Decode(&described)
	media, _ := parts.NextPart()
	body, _ := io.ReadAll(media)
	if match := query.Get("ifGenerationMatch"); match != "" {
		current := s.objects[bucket][described.Name]
		have := int64(0)
		if current != nil {
			have = current.generation
		}
		if strconv.FormatInt(have, 10) != match {
			w.WriteHeader(http.StatusPreconditionFailed)
			w.Write([]byte(`{"error":{"code":412,"message":"conditionNotMet"}}`))
			return
		}
	}
	stored := s.store(bucket, described.Name, body, described.ContentType, described.Metadata)
	stored.cache = described.CacheControl
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, s.resource(bucket, described.Name, stored))
}

type initiated struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadID string   `xml:"UploadId"`
}

type completion struct {
	Parts []struct {
		PartNumber int    `xml:"PartNumber"`
		ETag       string `xml:"ETag"`
	} `xml:"Part"`
}

func (s *storageServer) xml(w http.ResponseWriter, r *http.Request, path string) {
	bucket, escaped, _ := strings.Cut(path, "/")
	key, _ := url.PathUnescape(escaped)
	query := r.URL.Query()
	switch {
	case r.Method == http.MethodPost && query.Has("uploads"):
		s.nextUpload++
		id := fmt.Sprintf("upload-%d", s.nextUpload)
		metadata := map[string]string{}
		for name := range r.Header {
			if field, found := strings.CutPrefix(strings.ToLower(name), "x-goog-meta-"); found {
				metadata[field] = r.Header.Get(name)
			}
		}
		s.uploads[id] = multipartUpload{bucket: bucket, key: key, contentType: r.Header.Get("Content-Type"), metadata: metadata}
		w.Header().Set("Content-Type", "application/xml")
		_ = xml.NewEncoder(w).Encode(initiated{Bucket: bucket, Key: key, UploadID: id})
	case r.Method == http.MethodPost && query.Get("uploadId") != "":
		upload, found := s.uploads[query.Get("uploadId")]
		if !found {
			notFoundXML(w)
			return
		}
		var asked completion
		if err := xml.NewDecoder(r.Body).Decode(&asked); err != nil || len(asked.Parts) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if match := r.Header.Get("x-goog-if-generation-match"); match == "0" && s.objects[bucket][key] != nil {
			w.WriteHeader(http.StatusPreconditionFailed)
			w.Write([]byte(`<?xml version="1.0"?><Error><Code>PreconditionFailed</Code></Error>`))
			return
		}
		delete(s.uploads, query.Get("uploadId"))
		s.completed = append(s.completed, query.Get("uploadId"))
		s.store(bucket, key, []byte(strings.Repeat("p", len(asked.Parts))), upload.contentType, upload.metadata)
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(`<?xml version="1.0"?><CompleteMultipartUploadResult><Bucket>` + bucket + `</Bucket><Key>` + key + `</Key><ETag>"x-2"</ETag></CompleteMultipartUploadResult>`))
	case r.Method == http.MethodDelete && query.Get("uploadId") != "":
		if _, found := s.uploads[query.Get("uploadId")]; !found {
			notFoundXML(w)
			return
		}
		delete(s.uploads, query.Get("uploadId"))
		s.aborted = append(s.aborted, query.Get("uploadId"))
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func checksumOf(body []byte) string {
	sum := make([]byte, 4)
	binary.BigEndian.PutUint32(sum, crc32.Checksum(body, crc32.MakeTable(crc32.Castagnoli)))
	return base64.StdEncoding.EncodeToString(sum)
}

func notFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte(`{"error":{"code":404,"message":"No such object"}}`))
}

func notFoundXML(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte(`<?xml version="1.0"?><Error><Code>NoSuchUpload</Code></Error>`))
}

func writeJSON(w http.ResponseWriter, body any) {
	if err := json.NewEncoder(w).Encode(body); err != nil {
		panic(err)
	}
}
