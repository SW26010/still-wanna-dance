package cacheproxy

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckLocalNeverPublishesOrDeletes(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "videos")
	if err := os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	target := videoURL(payload)
	parser := &Server{cfg: DefaultConfig()}
	v, err := parser.parse(httptest.NewRequest("GET", target, nil))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cache, v.id+"-"+v.key+".mp4")
	for _, body := range []string{"broken", payload} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		hit, err := CheckLocal(context.Background(), root, target)
		if err != nil || hit != (body == payload) {
			t.Fatal(hit, err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != body {
			t.Fatal("scan modified cache", err)
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 1 {
			t.Fatal("scan created unexpected state", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CheckLocal(ctx, root, target); err != context.Canceled {
		t.Fatal(err)
	}
}
