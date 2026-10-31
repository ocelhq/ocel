package scope

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
)

func IsUserScope(scope scopev1.Scope) bool {
	return LeastRole(scope) != scopev1.Role_ROLE_UNSPECIFIED
}

func LeastRole(declared protoreflect.Enum) scopev1.Role {
	value := declared.Descriptor().Values().ByNumber(declared.Number())
	if value == nil {
		return scopev1.Role_ROLE_UNSPECIFIED
	}
	return proto.GetExtension(value.Options(), scopev1.E_LeastRole).(scopev1.Role)
}

func ClaimOf(declared protoreflect.Enum) string {
	value := declared.Descriptor().Values().ByNumber(declared.Number())
	if value == nil {
		return ""
	}
	return proto.GetExtension(value.Options(), scopev1.E_Claim).(string)
}

func RoleOfClaim(claim string) (scopev1.Role, bool) {
	if claim == "" {
		return scopev1.Role_ROLE_UNSPECIFIED, false
	}
	values := scopev1.Role(0).Descriptor().Values()
	for i := range values.Len() {
		role := scopev1.Role(values.Get(i).Number())
		if ClaimOf(role) == claim {
			return role, true
		}
	}
	return scopev1.Role_ROLE_UNSPECIFIED, false
}

func ScopeOfClaim(claim string) (scopev1.Scope, bool) {
	if claim == "" {
		return scopev1.Scope_SCOPE_UNSPECIFIED, false
	}
	values := scopev1.Scope(0).Descriptor().Values()
	for i := range values.Len() {
		scope := scopev1.Scope(values.Get(i).Number())
		if ClaimOf(scope) == claim {
			return scope, true
		}
	}
	return scopev1.Scope_SCOPE_UNSPECIFIED, false
}

func ListUserScopes() []scopev1.Scope {
	values := scopev1.Scope(0).Descriptor().Values()
	var scopes []scopev1.Scope
	for i := range values.Len() {
		if scope := scopev1.Scope(values.Get(i).Number()); IsUserScope(scope) {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

func RequiredScope(procedure string) (scopev1.Scope, error) {
	method, err := methodOf(procedure)
	if err != nil {
		return scopev1.Scope_SCOPE_UNSPECIFIED, err
	}
	scope := proto.GetExtension(method.Options(), scopev1.E_Required).(scopev1.Scope)
	if scope == scopev1.Scope_SCOPE_UNSPECIFIED {
		return scopev1.Scope_SCOPE_UNSPECIFIED, fmt.Errorf("%s declares no scope", procedure)
	}
	return scope, nil
}

func methodOf(procedure string) (protoreflect.MethodDescriptor, error) {
	service, name, found := strings.Cut(strings.TrimPrefix(procedure, "/"), "/")
	if !found || service == "" || name == "" || strings.Contains(name, "/") {
		return nil, fmt.Errorf("%q names no procedure", procedure)
	}
	described, err := serviceOf(service)
	if err != nil {
		return nil, err
	}
	method := described.Methods().ByName(protoreflect.Name(name))
	if method == nil {
		return nil, fmt.Errorf("%s declares no method %s", service, name)
	}
	return method, nil
}

func IsKnownService(name string) bool {
	_, err := serviceOf(name)
	return err == nil
}

func serviceOf(name string) (protoreflect.ServiceDescriptor, error) {
	found, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(name))
	if err != nil {
		return nil, fmt.Errorf("no service %s is known here", name)
	}
	service, ok := found.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, fmt.Errorf("%s names no service", name)
	}
	return service, nil
}

func ClaimsOf(scopes []scopev1.Scope) []string {
	claims := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if claim := ClaimOf(scope); claim != "" {
			claims = append(claims, claim)
		}
	}
	return claims
}
