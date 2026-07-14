package main

import (
	"net/url"
	"os"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

// newMigrate creates a new migrate instance pointing to the migrations directory.
//
// The path can be overridden via the MIGRATIONS_PATH env var to support local development
// (go run from repo root) and Docker deployments (migrations in a different location).
// The path is per-segment URL-escaped so spaces and URL-reserved characters yield a valid file:// URL.
func newMigrate(databaseURL string) (*migrate.Migrate, error) {
	migrationsPath := os.Getenv("MIGRATIONS_PATH")
	if migrationsPath == "" {
		migrationsPath = "internal/repository/postgres/migrations"
	}
	return migrate.New("file://"+encodePath(migrationsPath), databaseURL)
}

// encodePath URL-escapes each path segment so the result is safe to embed in
// a file:// URL. Leading slash (absolute path) and segment separators are
// preserved.
func encodePath(p string) string {
	leading := ""
	if strings.HasPrefix(p, "/") {
		leading = "/"
		p = strings.TrimPrefix(p, "/")
	}
	segments := strings.Split(p, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return leading + strings.Join(segments, "/")
}
