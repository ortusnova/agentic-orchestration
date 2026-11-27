// Compiled into Sortie's persistence package by test-migrate-stage0.sh
// (go test -overlay), so the migration's tests run against a database that
// Sortie's own migrations made, not a copy of its schema.
package persistence

import (
	"context"
	"os"
	"testing"
)

// TestKitMakeDatabase creates the database at ORTUS_NOVA_SORTIE_DB with
// every migration Sortie knows, and closes it.
func TestKitMakeDatabase(t *testing.T) {
	path := os.Getenv("ORTUS_NOVA_SORTIE_DB")
	if path == "" {
		t.Skip("ORTUS_NOVA_SORTIE_DB is not set")
	}
	ctx := context.Background()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
