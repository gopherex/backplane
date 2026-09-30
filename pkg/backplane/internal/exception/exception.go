// Package exception writes errors and panics as OpenTelemetry exception
// log records: exception.type, exception.message, exception.stacktrace and
// event.name=exception. The console's Errors section reads such records
// from the log store with no further setup.
package exception

import (
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/gopherex/xlog"
)

// Keys of the OpenTelemetry exception conventions.
const (
	Type       = "exception.type"
	Message    = "exception.message"
	Stacktrace = "exception.stacktrace"
	EventName  = "event.name"
	eventValue = "exception"
	panicType  = "panic"
)

// Fields describe err: its type is the deepest cause of a telling type
// (a sentinel's *errors.errorString and fmt's wrappers say nothing; none
// left: "error"), its stack the caller's (Go errors carry none).
func Fields(err error) []xlog.Field {
	kind := "error"

	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if name := fmt.Sprintf("%T", cause); !generic[name] {
			kind = name
		}
	}

	return fields(kind, err.Error(), debug.Stack())
}

//nolint:gochecknoglobals // immutable set
var generic = map[string]bool{
	"*errors.errorString": true, "*fmt.wrapError": true, "*fmt.wrapErrors": true, "*errors.joinError": true,
}

// Panic describes a recovered panic value with the stack it unwound.
func Panic(v any, stack []byte) []xlog.Field {
	kind := panicType
	if err, ok := v.(error); ok {
		kind = fmt.Sprintf("panic: %T", err)
	}

	return fields(kind, fmt.Sprint(v), stack)
}

func fields(kind, message string, stack []byte) []xlog.Field {
	return []xlog.Field{
		xlog.String(EventName, eventValue), xlog.String(Type, kind), xlog.String(Message, message),
		xlog.String(Stacktrace, string(stack)),
	}
}
