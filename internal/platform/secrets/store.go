// Package secrets provides a cross-platform credential store interface so raw
// API tokens and passwords are never written into config.yaml or SQLite.
package secrets

import "errors"

// ErrNotFound is returned when the requested secret reference does not exist.
var ErrNotFound = errors.New("secret not found")

// Store defines the minimal secret storage contract shared across Linux, macOS, and Windows.
type Store interface {
	BackendName() string
	Get(ref string) (string, error)
	Set(ref string, value string) error
	Delete(ref string) error
}
