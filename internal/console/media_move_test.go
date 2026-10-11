package console

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/cacheproxy"
)

func TestPortableMoveReopensInternalAndExternalMedia(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "internal", true: "external"}[external], func(t *testing.T) {
			root := t.TempDir()
			original := filepath.Join(root, "original")
			config := apppaths.DefaultConfig(filepath.Join(original, "SWD.exe"))
			c, err := New(config, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			settings := c.settings
			if external {
				settings.StorageDir = filepath.Join(root, "external media")
			}
			if err := c.save(settings); err != nil {
				t.Fatal(err)
			}
			c.Close()
			cfg := fixtureCacheConfig()
			cfg.StorageDir = settings.StorageDir
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
			video := filepath.Join("videos", strings.Repeat("a", 32)+".mp4")
			if err := os.WriteFile(filepath.Join(cfg.StorageDir, video), []byte("preserve video"), 0600); err != nil {
				t.Fatal(err)
			}
			database, err := os.ReadFile(filepath.Join(cfg.StorageDir, "storage.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			moved := filepath.Join(root, "moved")
			if err := os.Rename(original, moved); err != nil {
				t.Fatal(err)
			}
			c, err = New(apppaths.DefaultConfig(filepath.Join(moved, "SWD.exe")), "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			want := filepath.Join(moved, "data", "storage")
			if external {
				want = settings.StorageDir
			}
			if c.settings.StorageDir != want {
				t.Fatal("wrong library after move", c.settings.StorageDir)
			}
			got, err := os.ReadFile(filepath.Join(want, "storage.sqlite"))
			if err != nil || !bytes.Equal(got, database) {
				t.Fatal("database changed during move", err)
			}
			cfg.StorageDir = want
			engine, err = cacheproxy.New(cfg)
			if err != nil {
				t.Fatal("moved library could not reopen", err)
			}
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
			settings = c.settings
			settings.StorageDir = filepath.Join(root, "selected but unused")
			if err := c.save(settings); err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(filepath.Join(want, video)); err != nil || string(got) != "preserve video" {
				t.Fatal("changing settings moved or damaged media", err)
			}
			if _, err := os.Stat(settings.StorageDir); !os.IsNotExist(err) {
				t.Fatal("saving a path created another store", err)
			}
		})
	}
}
