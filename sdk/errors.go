package ocel

import "fmt"

// An UnprovisionedError is returned by every accessor of a resource during
// discovery, the pass that reads declarations before anything is provisioned. Match it
// with errors.As to keep a boot path alive when the resource is optional there.
type UnprovisionedError struct {
	// Resource is the declaration the accessor belongs to, as written in code.
	Resource string
	// Access is the accessor that was called.
	Access string
}

// Error reports which accessor reached for a resource that discovery has not
// provisioned.
func (e *UnprovisionedError) Error() string {
	return fmt.Sprintf(
		"'%s' cannot be used during discovery: tried to access '%s' before the resource was provisioned",
		e.Resource, e.Access,
	)
}

// A MissingBindingError is returned by every accessor of a resource whose binding was
// never delivered to the process. Match it with errors.As to tell a resource
// this deploy does not include from one that is misconfigured.
type MissingBindingError struct {
	// Key is the name the binding is delivered under, as a file in OCEL_LIVE_DIR or a live value.
	Key string
}

// Error names the key and the commands that deliver a binding into it.
func (e *MissingBindingError) Error() string {
	return fmt.Sprintf(
		"%s is not delivered to this process: `ocel dev` delivers it locally and `ocel deploy` "+
			"to the deployed app, and a build gets it only from an `ocel deploy` whose provider forwards "+
			"a port to the resource, so code that runs while building, such as prerendering a page, "+
			"cannot use it under `ocel build`",
		e.Key,
	)
}

// A RuntimeCallError is returned when the ocel runtime fails or refuses a call an
// accessor made. Match it with errors.As to read the Connect code the runtime
// answered with, without importing the transport.
type RuntimeCallError struct {
	// Procedure is the procedure that was called, as /<service>/<method>.
	Procedure string
	// Code is the Connect code the runtime answered with, in its wire form, such as
	// "not_found" or "unavailable".
	Code string
	// Err is the transport's own error.
	Err error
}

// Error names the procedure and the transport's error.
func (e *RuntimeCallError) Error() string {
	return fmt.Sprintf("ocel: %s: %v", e.Procedure, e.Err)
}

// Unwrap returns the transport's own error.
func (e *RuntimeCallError) Unwrap() error { return e.Err }
