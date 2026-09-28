// Package testlog provides loggers for tests.
package testlog

import (
	"io"

	"github.com/gopherex/xlog"
)

// Discard returns a logger that writes nowhere.
func Discard() *xlog.Logger { return xlog.NewJSON(xlog.WithWriter(io.Discard)) }
