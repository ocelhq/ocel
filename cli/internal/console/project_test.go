package console

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

type projectService struct {
	consolev1connect.UnimplementedProjectServiceHandler
	create    func(context.Context, *consolev1.CreateProjectRequest) (*consolev1.CreateProjectResponse, error)
	list      func(context.Context, *consolev1.ListProjectsRequest) (*consolev1.ListProjectsResponse, error)
	headers   http.Header
	procedure string
}

func (s *projectService) Create(ctx context.Context, req *consolev1.CreateProjectRequest) (*consolev1.CreateProjectResponse, error) {
	return s.create(ctx, req)
}

func (s *projectService) List(ctx context.Context, req *consolev1.ListProjectsRequest) (*consolev1.ListProjectsResponse, error) {
	return s.list(ctx, req)
}

func serveProjects(t *testing.T, service *projectService) *Client {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := consolev1connect.NewProjectServiceHandler(service)
	mux.Handle("/api/connect"+path, http.StripPrefix("/api/connect", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		service.headers = r.Header.Clone()
		service.procedure = r.URL.Path
		handler.ServeHTTP(w, r)
	})))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func TestCreateProjectSendsTheNameAndSlugAndCallsOnlyATakenSlugAConflict(t *testing.T) {
	t.Parallel()

	t.Run("sends the name and slug to ProjectService.Create under the console's connect route with the session as a bearer", func(t *testing.T) {
		t.Parallel()

		var got *consolev1.CreateProjectRequest
		service := &projectService{create: func(_ context.Context, req *consolev1.CreateProjectRequest) (*consolev1.CreateProjectResponse, error) {
			got = req
			return &consolev1.CreateProjectResponse{Project: &consolev1.Project{
				Id: "proj_1", OrganizationId: "org_1", Name: "My App", Slug: "my-app",
			}}, nil
		}}

		project, err := serveProjects(t, service).CreateProject(context.Background(), "tok", "My App", "my-app")
		if err != nil {
			t.Fatalf("CreateProject err = %v", err)
		}
		if service.procedure != consolev1connect.ProjectServiceCreateProcedure {
			t.Errorf("procedure = %s, want %s", service.procedure, consolev1connect.ProjectServiceCreateProcedure)
		}
		if gotAuth := service.headers.Get("Authorization"); gotAuth != "Bearer tok" {
			t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tok")
		}
		if want := (&consolev1.CreateProjectRequest{Name: "My App", Slug: "my-app"}); !proto.Equal(got, want) {
			t.Errorf("request = %v, want %v", got, want)
		}
		if project.GetId() != "proj_1" {
			t.Errorf("project id = %q, want proj_1", project.GetId())
		}
	})

	cases := []struct {
		name         string
		code         connect.Code
		wantConflict bool
	}{
		{name: "a taken slug is a conflict", code: connect.CodeAlreadyExists, wantConflict: true},
		{name: "a rejected session is not a conflict", code: connect.CodeUnauthenticated},
		{name: "an invalid name is not a conflict", code: connect.CodeInvalidArgument},
		{name: "any other failure is not a conflict", code: connect.CodeInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			service := &projectService{create: func(context.Context, *consolev1.CreateProjectRequest) (*consolev1.CreateProjectResponse, error) {
				return nil, connect.NewError(tc.code, fmt.Errorf("refused"))
			}}

			_, err := serveProjects(t, service).CreateProject(context.Background(), "tok", "My App", "my-app")
			if err == nil {
				t.Fatal("CreateProject err = nil, want error")
			}
			if got := IsConflict(err); got != tc.wantConflict {
				t.Fatalf("IsConflict(%v) = %t, want %t", err, got, tc.wantConflict)
			}
			wrapped := fmt.Errorf("create project: %w", err)
			if got := IsConflict(wrapped); got != tc.wantConflict {
				t.Fatalf("IsConflict(%v) = %t, want %t", wrapped, got, tc.wantConflict)
			}
		})
	}
}

func TestListProjectsReturnsEveryProjectAndRefusesARejectedSession(t *testing.T) {
	t.Parallel()

	t.Run("calls ProjectService.List with the session as a bearer and returns every project", func(t *testing.T) {
		t.Parallel()

		service := &projectService{list: func(context.Context, *consolev1.ListProjectsRequest) (*consolev1.ListProjectsResponse, error) {
			return &consolev1.ListProjectsResponse{Projects: []*consolev1.Project{
				{Id: "p1", OrganizationId: "org_1", Name: "My App", Slug: "my-app"},
				{Id: "p2", OrganizationId: "org_1", Name: "Other", Slug: "other"},
			}}, nil
		}}

		projects, err := serveProjects(t, service).ListProjects(context.Background(), "tok")
		if err != nil {
			t.Fatalf("ListProjects err = %v", err)
		}
		if service.procedure != consolev1connect.ProjectServiceListProcedure {
			t.Errorf("procedure = %s, want %s", service.procedure, consolev1connect.ProjectServiceListProcedure)
		}
		if gotAuth := service.headers.Get("Authorization"); gotAuth != "Bearer tok" {
			t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tok")
		}
		if len(projects) != 2 || projects[0].GetSlug() != "my-app" || projects[1].GetId() != "p2" {
			t.Fatalf("projects = %v, want the two listed", projects)
		}
	})

	t.Run("a rejected session is unauthenticated", func(t *testing.T) {
		t.Parallel()

		service := &projectService{list: func(context.Context, *consolev1.ListProjectsRequest) (*consolev1.ListProjectsResponse, error) {
			return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("A session is required"))
		}}

		_, err := serveProjects(t, service).ListProjects(context.Background(), "tok")
		if connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("ListProjects err = %v, want an unauthenticated error", err)
		}
	})
}
