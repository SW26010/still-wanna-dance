package cacheproxy

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"testing/synctest"
)

func readerTestBlocks(t *testing.T) (*blockFiles, string) {
	t.Helper()
	const body = "01234567ABCDEFGHxyz"
	f := newBlockFiles(t.TempDir(), int64(len(body)), 8, nil)
	t.Cleanup(f.release)
	for block := range blockCount(f.size, f.blockSize) {
		start := block * f.blockSize
		if err := f.store(block, []byte(body[start:min(start+f.blockSize, f.size)])); err != nil {
			t.Fatal(err)
		}
	}
	return f, body
}

func TestBlockReaderReleasesCurrentFile(t *testing.T) {
	for _, exit := range []string{"boundary", "seek", "close", "cancel", "read-error"} {
		t.Run(exit, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				blocks, body := readerTestBlocks(t)
				spool := (&localResource{storage: blocks}).makeSpool(nil)
				defer spool.release()
				ctx, cancel := context.WithCancelCause(context.Background())
				defer cancel(nil)
				reader := spool.reader(ctx, blocks.size)
				defer reader.Close()
				buffer := make([]byte, 2)
				if n, err := reader.Read(buffer); n != 2 || err != nil || string(buffer) != body[:2] {
					t.Fatal("initial read", n, err, string(buffer))
				}
				path := localBlockPath(t, blocks, 0)
				blocks.removeFiles(blocks.retire(func(int64) bool { return true }))
				entries, err := os.ReadDir(blocks.dir)
				if err != nil || len(entries) != 1 || blocks.has(0) {
					t.Fatal("reader must retain only its current file, without preserving availability", len(entries), err)
				}
				switch exit {
				case "boundary":
					rest := make([]byte, 6)
					if n, err := reader.Read(rest); n != 6 || err != nil || string(rest) != body[2:8] {
						t.Fatal("retired current file lost bytes", n, err, string(rest))
					}
				case "seek":
					if _, err := reader.Seek(4, io.SeekStart); err != nil {
						t.Fatal(err)
					}
					if _, err := reader.Read(buffer); err != nil || string(buffer) != body[4:6] {
						t.Fatal("seek within current block lost its pin", err)
					}
					if _, err := reader.Seek(8, io.SeekStart); err != nil {
						t.Fatal(err)
					}
				case "close":
					if err := reader.Close(); err != nil {
						t.Fatal(err)
					}
				case "cancel":
					cause := errors.New("client left")
					cancel(cause)
					synctest.Wait() // Cancellation must release an idle reader's file.
					if _, err := reader.Read(buffer); !errors.Is(err, cause) {
						t.Fatal("lost cancellation cause", err)
					}
				case "read-error":
					if err := os.Truncate(path, 1); err != nil {
						t.Fatal(err)
					}
					if _, err := reader.Read(buffer); !errors.Is(err, io.EOF) {
						t.Fatal("missing read failure", err)
					}
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("finished file reference prevented physical reclamation", err)
				}
			})
		})
	}
}

func TestBlockReaderOpenFailureReleasesPin(t *testing.T) {
	blocks, _ := readerTestBlocks(t)
	reader := blocks.reader()
	defer reader.Close()
	if err := os.Remove(localBlockPath(t, blocks, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadAt(make([]byte, 1), 0); !os.IsNotExist(err) {
		t.Fatal("lost file-open failure", err)
	}
	remove := blocks.retire(func(block int64) bool { return block == 0 })
	if len(remove) != 1 {
		t.Fatal("failed open retained a file pin")
	}
	blocks.removeFiles(remove)
}

func TestBlockReadersKeepIndependentCurrentFiles(t *testing.T) {
	blocks, body := readerTestBlocks(t)
	spool := (&localResource{storage: blocks}).makeSpool(nil)
	defer spool.release()
	one, two := spool.reader(context.Background(), blocks.size), spool.reader(context.Background(), blocks.size)
	defer one.Close()
	defer two.Close()
	buffer := make([]byte, 2)
	for _, reader := range []*spoolReader{one, two} {
		if _, err := reader.Read(buffer); err != nil {
			t.Fatal(err)
		}
	}
	path := localBlockPath(t, blocks, 0)
	blocks.removeFiles(blocks.retire(func(int64) bool { return true }))
	if err := one.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("one reader released another reader's file", err)
	}
	rest := make([]byte, 6)
	if _, err := two.Read(rest); err != nil || string(rest) != body[2:8] {
		t.Fatal("remaining reader lost bytes", err, string(rest))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("last reader kept the consumed file", err)
	}
}

func TestBlockSnapshotOutlivesStorage(t *testing.T) {
	blocks := newBlockFiles(t.TempDir(), 11, 8, nil)
	release := sync.OnceFunc(blocks.release)
	defer release()
	if err := blocks.store(0, []byte("01234567")); err != nil {
		t.Fatal(err)
	}
	if err := blocks.store(1, []byte("xyz")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := blocks.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	release()
	got, err := io.ReadAll(io.NewSectionReader(snapshot, 3, 8))
	if err != nil || string(got) != "34567xyz" {
		t.Fatal("snapshot lost pinned files after storage shutdown", string(got), err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.ReadAt(make([]byte, 1), 0); !errors.Is(err, os.ErrClosed) {
		t.Fatal("closed snapshot reused released pins", err)
	}
	entries, err := os.ReadDir(blocks.dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("snapshot left files after Close", len(entries), err)
	}
}

func TestBlockReaderConcurrentReadAndClose(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		name := "reader"
		if snapshot {
			name = "snapshot"
		}
		t.Run(name, func(t *testing.T) {
			blocks, body := readerTestBlocks(t)
			reader := blocks.reader()
			if snapshot {
				var err error
				reader, err = blocks.snapshot()
				if err != nil {
					t.Fatal(err)
				}
			}
			defer reader.Close()
			var workers sync.WaitGroup
			ready := make(chan struct{}, 8)
			start := make(chan struct{})
			for range 8 {
				workers.Go(func() {
					buffer := make([]byte, 1)
					if _, err := reader.ReadAt(buffer, 0); err != nil {
						t.Error(err)
					}
					ready <- struct{}{}
					<-start
					for offset := range int64(len(body)) {
						n, err := reader.ReadAt(buffer, offset)
						if errors.Is(err, os.ErrClosed) {
							return
						}
						if err != nil || n != 1 || buffer[0] != body[offset] {
							t.Errorf("concurrent read: offset=%d n=%d err=%v data=%q", offset, n, err, buffer)
							return
						}
					}
				})
			}
			for range 8 {
				<-ready
			}
			close(start)
			if err := reader.Close(); err != nil {
				t.Fatal(err)
			}
			workers.Wait()
		})
	}
}
