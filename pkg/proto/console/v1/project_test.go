package consolev1_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

func validCreateProjectRequest() *consolev1.CreateProjectRequest {
	return &consolev1.CreateProjectRequest{Name: "Shop", Slug: "shop"}
}

func TestProtovalidateAcceptsEveryProjectTheCLICanCreate(t *testing.T) {
	requireAccepted(t, "a name and a slug", validCreateProjectRequest())
	requireAccepted(t, "a slug of several words", &consolev1.CreateProjectRequest{Name: "Shop", Slug: "my-shop-2"})
	requireAccepted(t, "a description", &consolev1.CreateProjectRequest{Name: "Shop", Slug: "shop", Description: proto.String("the storefront")})
	requireAccepted(t, "a list", &consolev1.ListProjectsRequest{})
}

func TestProtovalidateRefusesAProjectTheConsoleCannotStore(t *testing.T) {
	requireRefusals(t, validCreateProjectRequest, []refusal[*consolev1.CreateProjectRequest]{
		{name: "the name is required", mutate: func(r *consolev1.CreateProjectRequest) { r.Name = "" }},
		{name: "the name is at most 100 characters", mutate: func(r *consolev1.CreateProjectRequest) { r.Name = strings.Repeat("n", 101) }},
		{name: "the slug is required", mutate: func(r *consolev1.CreateProjectRequest) { r.Slug = "" }},
		{name: "the slug is lower case", mutate: func(r *consolev1.CreateProjectRequest) { r.Slug = "Shop" }},
		{name: "the slug has no spaces", mutate: func(r *consolev1.CreateProjectRequest) { r.Slug = "my shop" }},
		{name: "the slug does not start with a dash", mutate: func(r *consolev1.CreateProjectRequest) { r.Slug = "-shop" }},
		{name: "the slug does not end with a dash", mutate: func(r *consolev1.CreateProjectRequest) { r.Slug = "shop-" }},
		{name: "the slug is at most 63 characters", mutate: func(r *consolev1.CreateProjectRequest) { r.Slug = strings.Repeat("s", 64) }},
	})
}
