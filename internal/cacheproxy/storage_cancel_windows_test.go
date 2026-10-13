//go:build windows

package cacheproxy

import (
	"context"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
)

var cancelQuerySequence atomic.Uint64

// Cancellation while the driver hands off Rows must release the statement.
// A leaked statement can keep SQLite's file open even after sql.DB.Close succeeds.
// Regression for https://gitlab.com/cznic/sqlite/-/merge_requests/81.
func TestCanceledStorageQueryReleasesFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	name := "swd_test_cancel_" + strconv.FormatUint(cancelQuerySequence.Add(1), 10)
	err := sqlite.RegisterScalarFunction(name, 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		close(entered)
		<-ctx.Done()
		// Give the driver's interrupt worker time to observe cancellation
		// before handing the completed result back to QueryContext.
		time.Sleep(10 * time.Millisecond)
		return int64(42), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "storage.sqlite")
	db, err := openStorageDatabase(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	done := make(chan error, 1)
	go func() {
		rows, err := db.QueryContext(ctx, "SELECT "+name+"() FROM sqlite_schema LIMIT 1")
		if rows != nil {
			rows.Close()
		}
		done <- err
	}()
	select {
	case <-entered:
		cancel()
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("query did not enter the controlled function")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("query cancellation: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("canceled query retained the closed database file: %v", err)
	}
}
