//go:build (darwin && (amd64 || arm64)) || (freebsd && (amd64 || arm64)) || (linux && (386 || amd64 || arm || arm64 || loong64 || ppc64le || riscv64 || s390x)) || (windows && (386 || amd64 || arm64))

package db

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func openDB(dbPath string) (*sql.DB, error) {
	debug := os.Getenv("CRUSH_DEBUG") != ""

	// For Akuma, ensure the path is simplified to avoid complex resolution issues.
	// If the directory doesn't exist, SQLite might fail with (14) if it's a URI.
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "crush: warning: failed to create db directory %q: %v\n", dir, err)
	}

	// Explicitly try to open/create the file to ensure it's accessible.
	// Some VFS implementations in Akuma might be picky about URI creation.
	f, err := os.OpenFile(dbPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		if debug {
			fmt.Fprintf(os.Stderr, "crush: debug: failed to create/open db file %q: %v\n", dbPath, err)
		}
	} else {
		f.Close()
	}

	absPath, err := filepath.Abs(dbPath)
	if err == nil {
		dbPath = absPath
	}

	// Set pragmas for better performance via _pragma query params.
	// Format: _pragma=name(value)
	params := url.Values{}
	for name, value := range pragmas {
		params.Add("_pragma", fmt.Sprintf("%s(%s)", name, value))
	}

	// Use a simpler DSN if URI fails, or just print for debugging
	dsn := fmt.Sprintf("file:%s?nolock=1&%s", dbPath, params.Encode())
	if debug {
		fmt.Fprintf(os.Stderr, "crush: debug: connecting to db: %s\n", dsn)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database %q: %w", dsn, err)
	}

	return db, nil
}
