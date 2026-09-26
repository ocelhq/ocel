package live

import (
	"bytes"
	"context"
	"crypto/rand"
	"net"
	"sort"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type firestoreServer struct {
	firestorepb.UnimplementedFirestoreServer

	mu   sync.Mutex
	docs map[string]*firestorepb.Document
}

func (s *firestoreServer) BatchGetDocuments(req *firestorepb.BatchGetDocumentsRequest, stream firestorepb.Firestore_BatchGetDocumentsServer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range req.GetDocuments() {
		var answer *firestorepb.BatchGetDocumentsResponse
		if doc, found := s.docs[name]; found {
			answer = &firestorepb.BatchGetDocumentsResponse{Result: &firestorepb.BatchGetDocumentsResponse_Found{Found: doc}}
		} else {
			answer = &firestorepb.BatchGetDocumentsResponse{Result: &firestorepb.BatchGetDocumentsResponse_Missing{Missing: name}}
		}
		answer.ReadTime = timestamppb.Now()
		if err := stream.Send(answer); err != nil {
			return err
		}
	}
	return nil
}

func (s *firestoreServer) RunQuery(req *firestorepb.RunQueryRequest, stream firestorepb.Firestore_RunQueryServer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	query := req.GetStructuredQuery()
	if len(query.GetFrom()) != 1 {
		return status.Error(codes.InvalidArgument, "one collection per query")
	}
	prefix := req.GetParent() + "/" + query.GetFrom()[0].GetCollectionId() + "/"
	var names []string
	for name := range s.docs {
		if strings.HasPrefix(name, prefix) && !strings.Contains(strings.TrimPrefix(name, prefix), "/") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if err := stream.Send(&firestorepb.RunQueryResponse{Document: s.docs[name], ReadTime: timestamppb.Now()}); err != nil {
			return err
		}
	}
	return stream.Send(&firestorepb.RunQueryResponse{ReadTime: timestamppb.Now()})
}

func (s *firestoreServer) BeginTransaction(context.Context, *firestorepb.BeginTransactionRequest) (*firestorepb.BeginTransactionResponse, error) {
	return &firestorepb.BeginTransactionResponse{Transaction: []byte("tx")}, nil
}

func (s *firestoreServer) Rollback(context.Context, *firestorepb.RollbackRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *firestoreServer) Commit(_ context.Context, req *firestorepb.CommitRequest) (*firestorepb.CommitResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := timestamppb.Now()
	results := make([]*firestorepb.WriteResult, 0, len(req.GetWrites()))
	for _, write := range req.GetWrites() {
		switch op := write.GetOperation().(type) {
		case *firestorepb.Write_Update:
			doc := op.Update
			doc.CreateTime, doc.UpdateTime = now, now
			s.docs[doc.GetName()] = doc
		case *firestorepb.Write_Delete:
			delete(s.docs, op.Delete)
		default:
			return nil, status.Errorf(codes.Unimplemented, "write %T", op)
		}
		results = append(results, &firestorepb.WriteResult{UpdateTime: now})
	}
	return &firestorepb.CommitResponse{WriteResults: results, CommitTime: now}, nil
}

type kmsServer struct {
	kmspb.UnimplementedKeyManagementServiceServer

	mu     sync.Mutex
	sealed map[string]sealedUnder
}

type sealedUnder struct {
	key       string
	aad       []byte
	plaintext []byte
}

func (s *kmsServer) Encrypt(_ context.Context, req *kmspb.EncryptRequest) (*kmspb.EncryptResponse, error) {
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sealed[string(token)] = sealedUnder{key: req.GetName(), aad: req.GetAdditionalAuthenticatedData(), plaintext: req.GetPlaintext()}
	return &kmspb.EncryptResponse{Name: req.GetName() + "/cryptoKeyVersions/1", Ciphertext: token}, nil
}

func (s *kmsServer) Decrypt(_ context.Context, req *kmspb.DecryptRequest) (*kmspb.DecryptResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sealed, found := s.sealed[string(req.GetCiphertext())]
	if !found || sealed.key != req.GetName() || !bytes.Equal(sealed.aad, req.GetAdditionalAuthenticatedData()) {
		return nil, status.Error(codes.InvalidArgument, "Decryption failed: the ciphertext is invalid")
	}
	return &kmspb.DecryptResponse{Plaintext: sealed.plaintext}, nil
}

func servingFirestoreAndKMS(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	firestorepb.RegisterFirestoreServer(server, &firestoreServer{docs: map[string]*firestorepb.Document{}})
	kmspb.RegisterKeyManagementServiceServer(server, &kmsServer{sealed: map[string]sealedUnder{}})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return "http://" + listener.Addr().String()
}
