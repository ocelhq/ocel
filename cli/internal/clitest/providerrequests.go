package clitest

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"
)

type ProviderRequests struct {
	mu     sync.Mutex
	bodies map[string][][]byte
}

func (r *ProviderRequests) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		message := body
		if strings.HasPrefix(req.Header.Get("Content-Type"), "application/connect+") && len(body) >= 5 {
			size := binary.BigEndian.Uint32(body[1:5])
			message = body[5 : 5+int(size)]
		}
		r.mu.Lock()
		if r.bodies == nil {
			r.bodies = map[string][][]byte{}
		}
		r.bodies[req.URL.Path] = append(r.bodies[req.URL.Path], message)
		r.mu.Unlock()
		next.ServeHTTP(w, req)
	})
}

func RequestsTo[M proto.Message](t *testing.T, r *ProviderRequests, procedure string) []M {
	t.Helper()

	r.mu.Lock()
	bodies := r.bodies[procedure]
	r.mu.Unlock()
	sent := make([]M, 0, len(bodies))
	for _, body := range bodies {
		var zero M
		message := zero.ProtoReflect().New().Interface().(M)
		if err := proto.Unmarshal(body, message); err != nil {
			t.Fatalf("decode a request to %s: %v", procedure, err)
		}
		sent = append(sent, message)
	}
	return sent
}
