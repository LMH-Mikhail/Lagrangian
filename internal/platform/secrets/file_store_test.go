package secrets

import (
	"errors"
	"testing"
)

func TestFileStoreRoundTrip(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if _, err := store.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := store.Set("api.token", "super-secret-value-123"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get("api.token")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "super-secret-value-123" {
		t.Fatalf("got %q, want %q", got, "super-secret-value-123")
	}
	if err := store.Delete("api.token"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get("api.token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}
