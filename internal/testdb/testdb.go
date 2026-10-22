// Package testdb opens the database the tests that need one run against.
package testdb

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/db"
)

// URL is TEST_DATABASE_URL, or the compose database by default.
func URL() string {
	if url := os.Getenv("TEST_DATABASE_URL"); url != "" {
		return url
	}
	return "postgres://deedflow:deedflow@127.0.0.1:5433/deedflow"
}

// Open connects to the test database and brings its tables up to date, so a
// fresh database (as in CI) works whichever package's tests run first. The
// test is skipped when the database can't be reached.
func Open(t *testing.T) *db.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d, err := db.Connect(ctx, URL())
	if err != nil {
		t.Skip("no test database: ", err)
	}
	t.Cleanup(d.Close)
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return d
}
