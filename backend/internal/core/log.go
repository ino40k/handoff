package core

import "handoff/internal/diagnostic"

// Keep backend callers stable while the Windows launcher shares only the
// lightweight diagnostic implementation, without importing networking state.
type Logger = diagnostic.Logger

func NewLogger(dir string) (*Logger, error) { return diagnostic.NewLogger(dir) }
func Redact(s string) string                { return diagnostic.Redact(s) }
