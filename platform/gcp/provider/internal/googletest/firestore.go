package googletest

import (
	"context"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Firestore struct {
	firestorepb.UnimplementedFirestoreServer

	URL string

	databases *Databases
	mu        sync.Mutex
	documents map[string]*firestorepb.Document
	unserved  int
	answered  int
}

func NewFirestore(t *testing.T, databases *Databases) *Firestore {
	t.Helper()
	fake := &Firestore{databases: databases, documents: map[string]*firestorepb.Document{}}
	fake.URL = ServeFirestore(t, fake, databases)
	return fake
}

func ServeFirestore(t *testing.T, data firestorepb.FirestoreServer, databases *Databases) string {
	t.Helper()
	grpcServer := grpc.NewServer()
	firestorepb.RegisterFirestoreServer(grpcServer, data)
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{Protocols: protocols, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		databases.Serve(t, w, r)
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return "http://" + listener.Addr().String()
}

func (f *Firestore) AnswerAbsent(calls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unserved = calls
}

func (f *Firestore) AnsweredAbsent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.answered
}

func (f *Firestore) refuseUnserved(name string) error {
	database := strings.SplitN(strings.TrimPrefix(name, "projects/"+Project+"/databases/"), "/", 2)[0]
	f.databases.Mu.Lock()
	held := f.databases.Held[database] != nil
	f.databases.Mu.Unlock()
	switch {
	case held && f.unserved == 0:
		return nil
	case held:
		f.unserved--
	}
	f.answered++
	return status.Errorf(codes.NotFound, "The database %s does not exist for project %s", database, Project)
}

func (f *Firestore) BeginTransaction(_ context.Context, req *firestorepb.BeginTransactionRequest) (*firestorepb.BeginTransactionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.refuseUnserved(req.GetDatabase()); err != nil {
		return nil, err
	}
	return &firestorepb.BeginTransactionResponse{Transaction: []byte("t1")}, nil
}

func (f *Firestore) Rollback(context.Context, *firestorepb.RollbackRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (f *Firestore) Commit(_ context.Context, req *firestorepb.CommitRequest) (*firestorepb.CommitResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.refuseUnserved(req.GetDatabase()); err != nil {
		return nil, err
	}
	now := timestamppb.Now()
	results := make([]*firestorepb.WriteResult, 0, len(req.GetWrites()))
	for _, write := range req.GetWrites() {
		switch {
		case write.GetUpdate() != nil:
			written := proto.Clone(write.GetUpdate()).(*firestorepb.Document)
			written.CreateTime, written.UpdateTime = now, now
			f.documents[written.GetName()] = written
		case write.GetDelete() != "":
			delete(f.documents, write.GetDelete())
		}
		results = append(results, &firestorepb.WriteResult{UpdateTime: now})
	}
	return &firestorepb.CommitResponse{WriteResults: results, CommitTime: now}, nil
}

func (f *Firestore) BatchGetDocuments(req *firestorepb.BatchGetDocumentsRequest, stream firestorepb.Firestore_BatchGetDocumentsServer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.refuseUnserved(req.GetDatabase()); err != nil {
		return err
	}
	for _, name := range req.GetDocuments() {
		answer := &firestorepb.BatchGetDocumentsResponse{Result: &firestorepb.BatchGetDocumentsResponse_Missing{Missing: name}, ReadTime: timestamppb.Now()}
		if held := f.documents[name]; held != nil {
			answer.Result = &firestorepb.BatchGetDocumentsResponse_Found{Found: held}
		}
		if err := stream.Send(answer); err != nil {
			return err
		}
	}
	return nil
}

func (f *Firestore) RunQuery(req *firestorepb.RunQueryRequest, stream firestorepb.Firestore_RunQueryServer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.refuseUnserved(req.GetParent()); err != nil {
		return err
	}
	query := req.GetStructuredQuery()
	collection := req.GetParent() + "/" + query.GetFrom()[0].GetCollectionId() + "/"
	var names []string
	for name := range f.documents {
		if id, held := strings.CutPrefix(name, collection); held && !strings.Contains(id, "/") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	if limit := query.GetLimit(); limit != nil {
		names = names[:min(len(names), int(limit.GetValue()))]
	}
	for _, name := range names {
		if err := stream.Send(&firestorepb.RunQueryResponse{Document: f.documents[name], ReadTime: timestamppb.Now()}); err != nil {
			return err
		}
	}
	return stream.Send(&firestorepb.RunQueryResponse{ReadTime: timestamppb.Now()})
}
