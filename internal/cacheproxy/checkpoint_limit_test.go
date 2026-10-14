package cacheproxy

import (
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"
)

func TestPausedCheckpointBoundsPreserveReaderReferences(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &Server{cfg: Config{Logger: log}}
	var first *rangeDownload
	for i := 0; i <= maxPausedFiles; i++ {
		f, err := os.CreateTemp(t.TempDir(), "download-*.part")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("prefix")); err != nil {
			t.Fatal(err)
		}
		d := &rangeDownload{storage: &spoolFile{file: f, refs: 1, log: log}}
		if i == 0 {
			first = d
			first.storage.retain()
		}
		for _, old := range s.retainPausedLocked(strconv.Itoa(i), d) {
			old.storage.release()
		}
	}
	if len(s.paused) != maxPausedFiles || s.paused["0"] != nil {
		t.Fatal("oldest checkpoint not evicted")
	}
	if _, err := first.storage.file.ReadAt(make([]byte, 6), 0); err != nil {
		t.Fatal("eviction broke existing reader", err)
	}
	path := first.storage.file.Name()
	first.storage.release()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("last reader left a file", err)
	}
	// Each file is six bytes. A twelve-byte budget must retain the newest two,
	// independently of the record limit. Production uses 512 MiB.
	for _, old := range s.trimPausedLocked(maxPausedFiles, 12) {
		old.storage.release()
	}
	if len(s.paused) != 2 || s.paused["31"] == nil || s.paused["32"] == nil {
		t.Fatal("byte limit did not evict oldest files")
	}
	for _, old := range s.trimPausedLocked(0, 0) {
		old.storage.release()
	}
}
