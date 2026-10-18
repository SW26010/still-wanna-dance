package cacheproxy

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"testing"
)

func localBlockPath(t *testing.T, blocks *blockFiles, block int64) string {
	t.Helper()
	blocks.mu.Lock()
	defer blocks.mu.Unlock()
	if blocks.files[block] == nil {
		t.Fatal("missing complete block", block)
	}
	return blocks.files[block].path
}

func TestLocalResourceLifetimePreservesCompleteBlocksAndReaders(t *testing.T) {
	s, cfg := setup(t, nil)
	var first *localResource
	var reader *spoolReader
	for i := range 40 {
		body := "complete block " + strconv.Itoa(i)
		r, _ := http.NewRequest("GET", videoURL(body), nil)
		v, _ := s.parseResolved(r)
		d, err := s.newLocalResource(v, &http.Response{Header: make(http.Header)}, s.cfg.Logger)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.storage.store(0, []byte(body)); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = d
			sp := d.makeSpool(nil)
			reader = sp.reader(context.Background(), v.size)
			sp.release()
		}
		d.storage.release()
	}
	defer reader.Close()
	if len(s.localResources) != 40 || s.localResources[first.video.key] == nil {
		t.Fatal("runtime block retention has an arbitrary resource limit")
	}
	wrong := first.video
	wrong.size++
	if _, err := s.acquireLocalResource(wrong); err == nil {
		t.Fatal("same key with a different size reused local blocks")
	}
	path := localBlockPath(t, first.storage, 0)
	s.closeLocalResources()
	data := make([]byte, first.video.size)
	if _, err := reader.Read(data); err != nil || string(data) != "complete block 0" {
		t.Fatal("registry cleanup broke an active reader", err)
	}
	reader.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("last reader left a block file", err)
	}
	assertNoPartial(t, cfg.tempDir())
}
