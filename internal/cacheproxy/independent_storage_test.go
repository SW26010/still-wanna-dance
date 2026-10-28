package cacheproxy

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func independentConfig(t *testing.T) Config {
	t.Helper()
	cfg := fixtureConfig()
	cfg.VideoDir = t.TempDir()
	var err error
	cfg.Store, err = CreateStore(filepath.Join(t.TempDir(), "library.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cfg.Store.Close(); err != nil {
			t.Error(err)
		}
	})
	cfg.Work, err = OpenWorkDirectory(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cfg.Work.Close(); err != nil {
			t.Error(err)
		}
	})
	return cfg
}

func TestEngineRequiresApplicationResources(t *testing.T) {
	for _, missing := range []string{"database", "work", "video"} {
		t.Run(missing, func(t *testing.T) {
			cfg := independentConfig(t)
			videoDir := cfg.VideoDir
			switch missing {
			case "database":
				cfg.Store = nil
			case "work":
				cfg.Work = nil
			case "video":
				cfg.VideoDir = ""
			}
			if engine, err := New(cfg); err == nil {
				engine.Close()
				t.Fatal("engine accepted incomplete application resources")
			}
			entries, err := os.ReadDir(videoDir)
			if err != nil || len(entries) != 0 {
				t.Fatal("rejected engine initialized video storage", entries, err)
			}
		})
	}
}

func TestIndependentVideoOwnershipAndMissingRoots(t *testing.T) {
	cfg := independentConfig(t)
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	other := independentConfig(t)
	other.VideoDir = cfg.VideoDir
	writeTestFile(t, filepath.Join(cfg.publishDir(), "download-publish-123.part"), "live stage")
	if second, err := New(other); err == nil {
		second.Close()
		t.Fatal("different database bypassed video ownership")
	}
	if got, err := os.ReadFile(filepath.Join(cfg.publishDir(), "download-publish-123.part")); err != nil || string(got) != "live stage" {
		t.Fatal("contending engine cleaned active stage", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// The video lease is reusable; neither engine closes its borrowed database.
	second, err := New(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Store.SetSongPolicy(t.Context(), 42, SongFavorite); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "disconnected", "complete-videos")
	other.VideoDir = missing
	if engine, err := New(other); err == nil {
		engine.Close()
		t.Fatal("missing video root accepted")
	}
	if _, err := os.Stat(filepath.Dir(missing)); !os.IsNotExist(err) {
		t.Fatal("engine recreated missing root", err)
	}
}

func TestIndependentVideoLeaseDoesNotLockSiblingDatabase(t *testing.T) {
	cfg := independentConfig(t)
	root := t.TempDir()
	cfg.VideoDir = filepath.Join(root, "videos")
	if err := os.Mkdir(cfg.VideoDir, 0700); err != nil {
		t.Fatal(err)
	}
	other, err := CreateStore(filepath.Join(root, "storage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	engine, err := New(cfg)
	if err != nil {
		t.Fatal("independent database blocked the video directory", err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentPathsRejectCleanupOverCoreData(t *testing.T) {
	names := []string{filepath.Join(".swd-tmp", "download-publish-123.part"), strings.Repeat("a", 32) + ".mp4", ".swd.lock"}
	if runtime.GOOS == "windows" {
		names = append(names, strings.Repeat("A", 32)+".mp4", strings.Repeat("a", 32)+".MP4", strings.Repeat("aA", 16)+".Mp4")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			cfg := independentConfig(t)
			store, err := CreateStore(filepath.Join(cfg.VideoDir, name))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })
			cfg.Store = store
			before, err := os.ReadFile(store.path)
			if err != nil {
				t.Fatal(err)
			}
			if engine, err := New(cfg); err == nil {
				engine.Close()
				t.Fatal("database could be treated as disposable media")
			}
			after, err := os.ReadFile(store.path)
			if err != nil || string(before) != string(after) {
				t.Fatal("rejected layout changed database", err)
			}
		})
	}
	cfg := independentConfig(t)
	cfg.VideoDir = cfg.Work.Path()
	if engine, err := New(cfg); err == nil {
		engine.Close()
		t.Fatal("complete videos overlapped block workspace")
	}
}

func TestIndependentLibraryReadsAndDeletionScope(t *testing.T) {
	cfg := independentConfig(t)
	key := strings.Repeat("a", 32)
	if err := cfg.Store.SyncCatalog(t.Context(), testCandidate("20261026000000", CatalogSong{ID: 42, MD5: key})); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, cfg.videoFile(key), "shared complete video")
	paths := cfg.libraryPaths()
	page, err := ReadLibraryPage(t.Context(), paths, LibraryQuery{})
	if err != nil || page.Total != 1 || page.VideoError != "" || page.FileCount != 1 {
		t.Fatal(page, err)
	}
	entry := page.Resources[key].Local
	if entry == nil {
		t.Fatal("video not found at selected location")
	}
	// Same bytes and song metadata, different database: old page stamp cannot
	// delete a file under the new database's associations and preferences.
	other := independentConfig(t)
	if err := other.Store.SyncCatalog(t.Context(), testCandidate("20261026000000", CatalogSong{ID: 42, MD5: key})); err != nil {
		t.Fatal(err)
	}
	other.VideoDir = cfg.VideoDir
	if CacheStorageID(paths) == CacheStorageID(other.libraryPaths()) {
		t.Fatal("database change did not invalidate scope")
	}
	if err := other.Store.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := DeleteCacheOffline(t.Context(), other.libraryPaths(), []CacheSelection{{Key: key, Stamp: entry.Stamp}})
	if err != nil || len(result) != 1 || result[0].Result != "changed" {
		t.Fatal(result, err)
	}
	if _, err := os.Stat(cfg.videoFile(key)); err != nil {
		t.Fatal("stale confirmation deleted video", err)
	}
	paths.VideoDir = filepath.Join(t.TempDir(), "missing")
	page, err = ReadLibraryPage(t.Context(), paths, LibraryQuery{})
	if err != nil || page.Total != 1 || page.VideoError == "" || page.Resources[key].Local != nil {
		t.Fatal("database view depended on video availability", page, err)
	}
	if page.DatabaseID != DatabaseID(cfg.Store.path) {
		t.Fatal("database scope changed with videos")
	}
	if _, err := ReadLibraryPage(t.Context(), paths, LibraryQuery{Filter: "missing"}); err == nil {
		t.Fatal("unknown availability was treated as missing")
	}
	songs, err := ReadSongPage(t.Context(), paths, "", "", 0)
	if err != nil || songs.Total != 1 || songs.VideoError == "" {
		t.Fatal("preferences depend on video availability", songs, err)
	}
	if _, err := CheckReferencedFiles(t.Context(), paths.VideoDir, map[string]string{"42": key}); err == nil {
		t.Fatal("scan treated missing root as an empty inventory")
	}
}
