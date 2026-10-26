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
			c, err := newConsoleWithWorkForTest(t, config, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			settings := c.settings
			settings.DatabasePath, settings.VideoDir = c.defaultDatabasePath(), c.defaultVideoDir()
			if external {
				setTestLibraryPaths(&settings, filepath.Join(root, "external media"))
				createTestDatabase(t, settings.DatabasePath)
			}
			if err := c.save(settings); err != nil {
				t.Fatal(err)
			}
			c.Close()
			cfg := fixtureCacheConfig()
			cfg.VideoDir = settings.VideoDir
			video := strings.Repeat("a", 32) + ".mp4"
			if err := os.WriteFile(filepath.Join(cfg.VideoDir, video), []byte("preserve video"), 0600); err != nil {
				t.Fatal(err)
			}
			database, err := os.ReadFile(settings.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			moved := filepath.Join(root, "moved")
			if err := os.Rename(original, moved); err != nil {
				t.Fatal(err)
			}
			c, err = newConsoleWithWorkForTest(t, apppaths.DefaultConfig(filepath.Join(moved, "SWD.exe")), "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			want := filepath.Join(moved, "data", "database")
			wantVideo := filepath.Join(moved, "videos")
			if external {
				want = testStorageRoot(settings)
				wantVideo = settings.VideoDir
			}
			if testStorageRoot(c.settings) != want || c.settings.VideoDir != wantVideo {
				t.Fatal("wrong library after move", testStorageRoot(c.settings))
			}
			got, err := os.ReadFile(filepath.Join(want, "storage.sqlite"))
			if err != nil || !bytes.Equal(got, database) {
				t.Fatal("database changed during move", err)
			}
			cfg.Store, cfg.Work, cfg.VideoDir = c.store, c.work, c.settings.VideoDir
			engine, err := cacheproxy.New(cfg)
			if err != nil {
				t.Fatal("moved library could not reopen", err)
			}
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
			settings = c.settings
			setTestLibraryPaths(&settings, filepath.Join(root, "selected but unused"))
			if err := c.save(settings); err == nil {
				t.Fatal("missing database selection accepted")
			}
			if got, err := os.ReadFile(filepath.Join(wantVideo, video)); err != nil || string(got) != "preserve video" {
				t.Fatal("changing settings moved or damaged media", err)
			}
			if _, err := os.Stat(testStorageRoot(settings)); !os.IsNotExist(err) {
				t.Fatal("saving a path created another store", err)
			}
		})
	}
}
