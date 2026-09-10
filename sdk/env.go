package ocel

import (
	"encoding"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ocelhq/ocel/pkg/constants"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

const (
	envTag          = "ocel"
	deliveredPrefix = "OCEL_VAR_"
	reservedPrefix  = "OCEL_"
	folderSeparator = ";"
)

var (
	keyPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

	ownerMu sync.Mutex
	owner   = map[string]string{}
)

// An EnvDefinitionError is what [Env] panics with when a struct declares a
// variable Ocel cannot accept: an unusable key, a class that contradicts its
// default, a field type nothing can parse a value into.
type EnvDefinitionError struct {
	// Key is the variable the problem is with, or "" when it is the struct's.
	Key string
	// Detail says what is wrong and how to fix it.
	Detail string
}

// Error names the key and the problem.
func (e *EnvDefinitionError) Error() string {
	if e.Key == "" {
		return e.Detail
	}
	return fmt.Sprintf("'%s' %s", e.Key, e.Detail)
}

// An EnvValueError is what [Env] panics with when a declared variable has no
// value, or one its field type rejects, and what [DeploymentURL] returns when
// no url was delivered.
type EnvValueError struct {
	// Key is the variable the value belongs to.
	Key string
	// Detail says what is wrong and how to fix it.
	Detail string
}

// Error names the key and the problem.
func (e *EnvValueError) Error() string {
	return fmt.Sprintf("'%s' %s", e.Key, e.Detail)
}

// An EnvScopeError is what [Env] panics with when a variable is scoped to
// folders this app is not bound to.
type EnvScopeError struct {
	// Key is the scoped variable.
	Key string
	// Folders is the scope the variable was declared with.
	Folders []string
	// Binding is the folder this app is bound to, "" at the project root.
	Binding string
}

// Error names the scope, the binding, and the two ways to reconcile them.
func (e *EnvScopeError) Error() string {
	binding := e.Binding
	if binding == "" {
		binding = "the project root"
	}
	return fmt.Sprintf(
		"'%s' is scoped to %s, but this app is bound to %s. Bind this app to one of those folders in ocel.config.ts, or widen the variable's scope.",
		e.Key, strings.Join(e.Folders, ", "), binding,
	)
}

// A Secret is a live variable: one whose value can be rotated underneath the
// running process. A field of this type declares the variable under the
// secret class, and [Secret.Value] resolves the value on every call rather
// than once at init, so a rotation reaches the next call. Printing a Secret
// with any fmt verb prints a redaction, never the value.
type Secret struct {
	key string
}

// Key is the variable the secret was declared under.
func (s Secret) Key() string { return s.key }

// Value is the secret's current value. It panics with an [*EnvValueError]
// when no value stands for the key any more, the way the read of a missing
// variable fails, rather than hand back an empty secret.
func (s Secret) Value() string {
	value, ok := readDelivered(s.key)
	if !ok {
		panic(unset(s.key))
	}
	return value
}

// String is a redaction naming the key, so a Secret never prints its value.
func (s Secret) String() string { return "Secret(" + s.key + ")" }

// GoString is the same redaction for the %#v verb.
func (s Secret) GoString() string { return s.String() }

type variable struct {
	key         string
	class       resourcesv1.VariableClass
	fallback    *string
	folders     []string
	description string
	field       reflect.StructField
	index       int
}

func (v variable) required() bool {
	return v.fallback == nil && v.field.Type.Kind() != reflect.Pointer
}

func (v variable) parsed() bool {
	return v.field.Type != reflect.TypeFor[string]() && !v.live()
}

func (v variable) confidential() bool {
	return v.class != resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN
}

func (v variable) live() bool {
	return v.field.Type == secretType
}

func (v variable) complaint(message string) string {
	if !v.confidential() {
		return message
	}
	return fmt.Sprintf("withheld, because a '%s' value's parse message can quote the value itself", className(v.class))
}

func className(class resourcesv1.VariableClass) string {
	return strings.ToLower(strings.TrimPrefix(class.String(), "VARIABLE_CLASS_"))
}

// Env declares the variables the fields of T name and returns T with every
// field set from the value delivered for it. Call it from a file under the
// project's discovery folder, the way [Postgres] is called: during discovery the
// call is the declaration and the struct comes back zero, since no value is
// resolved before the requirements are declared; at runtime each field is read
// from the environment the deploy delivered.
//
// Every field carries an `ocel` tag naming the variable, then any of:
//
//	ocel:"DATABASE_URL"                      plain, required
//	ocel:"API_KEY,sensitive"                 encrypted at rest, delivered baked
//	ocel:"PORT,default=3000"                 the value when none is set
//	ocel:"FEATURE_FLAG,folders=/apps/web"    one value per folder, ';' separated
//	ocel:"API_KEY,description=The API key"   an optional, one-line description
//
// A field of type [Secret] declares the secret class: encrypted at rest,
// delivered live, and read through [Secret.Value] on each use rather than
// copied into the struct, since the value can rotate underneath the process.
//
// A pointer field is optional and stays nil when nothing is set. Any other
// field is required unless it carries a default. A field may be a string, a
// bool, any integer or float type, a [time.Duration], or a type implementing
// [encoding.TextUnmarshaler]; the delivered text is parsed into it, and a
// value the type rejects fails the read. A class other than plain keeps the
// value out of every error, since a parser's message can quote it.
//
// Env panics with an [*EnvDefinitionError] when the struct declares something
// Ocel cannot accept, with an [*EnvValueError] when a value is missing or
// unparseable, and with an [*EnvScopeError] when a variable's folders leave
// this app out.
func Env[T any]() T {
	var out T
	_, file, line, _ := runtime.Caller(1)

	vars, err := definitions(reflect.TypeFor[T](), file)
	if err != nil {
		panic(err)
	}

	if discovering() {
		if err := declareEnv(vars, fmt.Sprintf("%s:%d", file, line)); err != nil {
			panic(fmt.Sprintf("ocel: declare env: %v", err))
		}
		return out
	}

	if err := resolve(reflect.ValueOf(&out).Elem(), vars); err != nil {
		panic(err)
	}
	return out
}

func definitions(t reflect.Type, source string) ([]variable, error) {
	if t.Kind() != reflect.Struct {
		return nil, &EnvDefinitionError{Detail: fmt.Sprintf("Env wants a struct type, and %s is a %s.", t, t.Kind())}
	}

	ownerMu.Lock()
	defer ownerMu.Unlock()

	var vars []variable
	for i := range t.NumField() {
		field := t.Field(i)
		v, err := definition(field, i)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(vars, func(seen variable) bool { return seen.key == v.key }) {
			return nil, &EnvDefinitionError{Key: v.key, Detail: "is declared by two fields of the same struct. A key is declared by exactly one field."}
		}
		if claimed, ok := owner[v.key]; ok && claimed != source {
			return nil, &EnvDefinitionError{Key: v.key, Detail: fmt.Sprintf("is already declared in %s. A key may be defined by exactly one file.", claimed)}
		}
		vars = append(vars, v)
	}
	for _, v := range vars {
		owner[v.key] = source
	}
	return vars, nil
}

func definition(field reflect.StructField, index int) (variable, error) {
	tag, ok := field.Tag.Lookup(envTag)
	if !ok {
		return variable{}, &EnvDefinitionError{Detail: fmt.Sprintf("field %s carries no `%s` tag. Every field of an Env struct names the variable it reads.", field.Name, envTag)}
	}
	if !field.IsExported() {
		return variable{}, &EnvDefinitionError{Detail: fmt.Sprintf("field %s is unexported, so nothing outside the package could read it. Export it.", field.Name)}
	}

	parts := strings.Split(tag, ",")
	v := variable{key: parts[0], class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, field: field, index: index}
	if !keyPattern.MatchString(v.key) {
		return variable{}, &EnvDefinitionError{Key: v.key, Detail: "is not a usable variable name: use upper-case letters, digits and underscores, starting with a letter or underscore."}
	}

	if v.live() {
		v.class = resourcesv1.VariableClass_VARIABLE_CLASS_SECRET
	}

	for _, option := range parts[1:] {
		name, value, assigned := strings.Cut(option, "=")
		switch {
		case option == "secret":
			return variable{}, &EnvDefinitionError{Key: v.key, Detail: "is tagged secret. The secret class is declared by the field's type: make it an ocel.Secret and drop the option."}
		case (option == "plain" || option == "sensitive") && v.live():
			return variable{}, &EnvDefinitionError{Key: v.key, Detail: fmt.Sprintf("is an ocel.Secret tagged '%s'. A Secret field is always the secret class; drop the option.", option)}
		case option == "plain":
			v.class = resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN
		case option == "sensitive":
			v.class = resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE
		case assigned && name == "default":
			v.fallback = &value
		case assigned && name == "folders":
			v.folders = strings.Split(value, folderSeparator)
		case assigned && name == "description":
			v.description = value
		default:
			return variable{}, &EnvDefinitionError{Key: v.key, Detail: fmt.Sprintf("has an unknown tag option '%s'. The options are plain, sensitive, default=<VALUE>, folders=<PATH;PATH> and description=<TEXT>.", option)}
		}
	}
	if problem := descriptionProblem(v.description); problem != "" {
		return variable{}, &EnvDefinitionError{Key: v.key, Detail: "has an unusable description: " + problem}
	}

	if v.key == constants.AppURLEnvName {
		return variable{}, &EnvDefinitionError{Key: v.key, Detail: "is written by Ocel for every app, from the hostname the deploy serves it on, so a declared one would be overwritten before anything read it. Read it with DeploymentURL."}
	}
	if !v.confidential() && strings.HasPrefix(v.key, reservedPrefix) {
		return variable{}, &EnvDefinitionError{Key: v.key, Detail: fmt.Sprintf("starts with the reserved prefix %s. A '%s' variable is delivered under its own name, so Ocel would overwrite it.", reservedPrefix, className(v.class))}
	}
	if v.fallback != nil && v.live() {
		return variable{}, &EnvDefinitionError{Key: v.key, Detail: "is a Secret with a default. A live value must fail loudly when it is missing rather than fall back."}
	}
	if field.Type == reflect.PointerTo(secretType) {
		return variable{}, &EnvDefinitionError{Key: v.key, Detail: "is an optional Secret. A live value must fail loudly when it is missing rather than fall back; declare it as an ocel.Secret."}
	}
	if v.folders != nil {
		if problem := scopeProblem(v.folders); problem != "" {
			return variable{}, &EnvDefinitionError{Key: v.key, Detail: "has an unusable folder scope: " + problem}
		}
	}
	if !parseable(field.Type) {
		return variable{}, &EnvDefinitionError{Key: v.key, Detail: fmt.Sprintf("is read into a %s, which nothing parses a value into. Use a string, bool, integer, float, time.Duration, encoding.TextUnmarshaler, a pointer to one, or an ocel.Secret.", field.Type)}
	}
	if v.fallback != nil {
		if _, err := parse(field.Type, *v.fallback); err != nil {
			return variable{}, &EnvDefinitionError{Key: v.key, Detail: fmt.Sprintf("has a default its own type rejects: %s.", v.complaint(err.Error()))}
		}
	}
	return v, nil
}

func descriptionProblem(description string) string {
	if len(description) > 120 {
		return "a description is at most 120 bytes."
	}
	if strings.IndexFunc(description, unicode.IsControl) >= 0 {
		return "a description is one line and has no control characters."
	}
	return ""
}

func scopeProblem(folders []string) string {
	if len(folders) == 0 {
		return "an empty folder scope says nothing. Leave 'folders' off to keep the variable at the project root."
	}
	seen := map[string]bool{}
	for _, folder := range folders {
		if seen[folder] {
			return fmt.Sprintf("folder '%s' is named twice. A scoped variable holds one value per folder it names.", folder)
		}
		seen[folder] = true
		if problem := folderProblem(folder); problem != "" {
			return fmt.Sprintf("folder '%s': %s", folder, problem)
		}
	}
	return ""
}

func folderProblem(folder string) string {
	switch {
	case !strings.HasPrefix(folder, "/"):
		return "a folder path must start with '/'."
	case folder == "/":
		return "'/' is the project root, which is what an unscoped variable already uses. Leave 'folders' off instead."
	case strings.HasSuffix(folder, "/"):
		return "a folder path must not end with '/'."
	case strings.Contains(folder, "//"):
		return "a folder path has no empty segments."
	case strings.Contains(folder, "#"):
		return "a folder path may not contain '#'."
	}
	return ""
}

func declareEnv(vars []variable, source string) error {
	req := &resourcesv1.DeclareEnvRequest{}
	for _, v := range vars {
		req.Definitions = append(req.Definitions, &resourcesv1.VariableDefinition{
			Key:         v.key,
			Class:       v.class,
			Required:    v.required(),
			Folders:     v.folders,
			Source:      source,
			HasSchema:   v.parsed(),
			Description: v.description,
		})
	}
	res, err := declareVariables(req)
	if err != nil {
		return err
	}
	problems := validate(vars, res.GetCells())
	if len(problems) == 0 {
		return nil
	}
	return reportEnvProblems(&resourcesv1.ReportEnvProblemsRequest{Problems: problems})
}

func validate(vars []variable, cells []*resourcesv1.VariableCell) []*resourcesv1.VariableProblem {
	var problems []*resourcesv1.VariableProblem
	for _, v := range vars {
		stored := slices.DeleteFunc(slices.Clone(cells), func(c *resourcesv1.VariableCell) bool { return c.GetKey() != v.key })

		if v.required() {
			for _, folder := range requiredFolders(v) {
				if !slices.ContainsFunc(stored, func(c *resourcesv1.VariableCell) bool { return c.GetFolder() == folder }) {
					problems = append(problems, problem(v.key, folder, resourcesv1.VariableProblem_KIND_MISSING, ""))
				}
			}
		}

		if v.live() || !v.parsed() {
			continue
		}
		for _, cell := range stored {
			if _, err := parse(v.field.Type, cell.GetValue()); err != nil {
				problems = append(problems, problem(v.key, cell.GetFolder(), resourcesv1.VariableProblem_KIND_INVALID, v.complaint(err.Error())))
			}
		}
	}
	return problems
}

func requiredFolders(v variable) []string {
	if len(v.folders) > 0 {
		return v.folders
	}
	return []string{""}
}

func problem(key, folder string, kind resourcesv1.VariableProblem_Kind, detail string) *resourcesv1.VariableProblem {
	return &resourcesv1.VariableProblem{Key: key, Folder: folder, Kind: kind, Detail: detail}
}

func resolve(target reflect.Value, vars []variable) error {
	for _, v := range vars {
		if !inScope(v.folders) {
			return &EnvScopeError{Key: v.key, Folders: v.folders, Binding: os.Getenv(constants.AppFolderEnvName)}
		}
		raw, ok := readDelivered(v.key)
		if ok && v.live() {
			target.Field(v.index).Set(reflect.ValueOf(Secret{key: v.key}))
			continue
		}
		if !ok && v.fallback != nil {
			raw, ok = *v.fallback, true
		}
		if !ok {
			if v.field.Type.Kind() == reflect.Pointer {
				continue
			}
			return unset(v.key)
		}
		value, err := parse(v.field.Type, raw)
		if err != nil {
			return &EnvValueError{Key: v.key, Detail: fmt.Sprintf("is set but does not satisfy its type: %s. Fix it with `ocel env set %s=<VALUE>`.", v.complaint(err.Error()), v.key)}
		}
		target.Field(v.index).Set(value)
	}
	return nil
}

func unset(key string) *EnvValueError {
	return &EnvValueError{Key: key, Detail: fmt.Sprintf("has no value. Set one with `ocel env set %s=<VALUE>`.", key)}
}

func inScope(folders []string) bool {
	return len(folders) == 0 || slices.Contains(folders, os.Getenv(constants.AppFolderEnvName))
}

func readDelivered(key string) (string, bool) {
	if value, ok := os.LookupEnv(deliveredPrefix + key); ok {
		return value, true
	}
	return os.LookupEnv(key)
}

var (
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
	durationType    = reflect.TypeFor[time.Duration]()
	secretType      = reflect.TypeFor[Secret]()
)

func parseable(t reflect.Type) bool {
	if t == secretType {
		return true
	}
	if t.Kind() == reflect.Pointer {
		return parseable(t.Elem())
	}
	if reflect.PointerTo(t).Implements(textUnmarshaler) || t == durationType {
		return true
	}
	switch t.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

func parse(t reflect.Type, raw string) (reflect.Value, error) {
	if t.Kind() == reflect.Pointer {
		inner, err := parse(t.Elem(), raw)
		if err != nil {
			return reflect.Value{}, err
		}
		ptr := reflect.New(t.Elem())
		ptr.Elem().Set(inner)
		return ptr, nil
	}

	value := reflect.New(t)
	if unmarshaler, ok := value.Interface().(encoding.TextUnmarshaler); ok {
		if err := unmarshaler.UnmarshalText([]byte(raw)); err != nil {
			return reflect.Value{}, err
		}
		return value.Elem(), nil
	}
	if t == durationType {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(d).Convert(t), nil
	}

	out := value.Elem()
	switch t.Kind() {
	case reflect.String:
		out.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return reflect.Value{}, err
		}
		out.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i, err := strconv.ParseInt(raw, 10, t.Bits())
		if err != nil {
			return reflect.Value{}, err
		}
		out.SetInt(i)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u, err := strconv.ParseUint(raw, 10, t.Bits())
		if err != nil {
			return reflect.Value{}, err
		}
		out.SetUint(u)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, t.Bits())
		if err != nil {
			return reflect.Value{}, err
		}
		out.SetFloat(f)
	}
	return out, nil
}

// DeploymentURL is the absolute url this app is served on, scheme and all:
// the deployed hostname, or the local one under `ocel dev`. Ocel writes it;
// nothing declares it. It fails with an [*EnvValueError] when the deploy gave
// this app no hostname.
func DeploymentURL() (string, error) {
	url, ok := readDelivered(constants.AppURLEnvName)
	if !ok {
		return "", &EnvValueError{
			Key:    constants.AppURLEnvName,
			Detail: "was not delivered to this app. Ocel writes it from the hostname the deploy serves the app on, and this app is served on none: add one under `domains.production` on the app, or on the project if this is the first app it names, and deploy again.",
		}
	}
	return url, nil
}
