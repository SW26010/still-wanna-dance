package cacheproxy

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestIndependentCrossVolumePublicationAndImport(t *testing.T) {
	root := os.Getenv("SWD_IMPORT_TEST_VOLUME")
	if root == "" {
		t.Skip("set SWD_IMPORT_TEST_VOLUME to an isolated directory on a second NTFS volume")
	}
	videoDir, err := os.MkdirTemp(root, "swd-independent-video-")
	if err != nil {
		t.Fatal(err)
	}
	// Only this randomly created directory is owned by the test.
	t.Cleanup(func() {
		if err := os.RemoveAll(videoDir); err != nil {
			t.Error(err)
		}
	})
	cfg := independentConfig(t)
	cfg.VideoDir = videoDir
	engine, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Close() })
	flight, resource := completedRangeFlight(t, engine, payload)
	block := localBlockPath(t, flight.ranges.storage, 0)
	probe := filepath.Join(videoDir, "volume-probe")
	if err := os.Link(block, probe); !errors.Is(err, windows.ERROR_NOT_SAME_DEVICE) {
		if err == nil {
			os.Remove(probe)
		}
		t.Fatal("fixture did not prove distinct NTFS volumes", err)
	}
	if err := engine.publishBlocks(t.Context(), cfg.videoFile(resource.key), resource, flight); err != nil {
		t.Fatal("cross-volume block publication", err)
	}
	if got, err := os.ReadFile(cfg.videoFile(resource.key)); err != nil || string(got) != payload {
		t.Fatal("published bytes changed", err)
	}
	if got, err := os.ReadFile(block); err != nil || string(got) != payload {
		t.Fatal("publication removed a referenced block", err)
	}
	// The staging directory must share the video's volume, unlike block storage.
	stageProbe := filepath.Join(cfg.publishDir(), "volume-probe")
	if err := os.Link(cfg.videoFile(resource.key), stageProbe); err != nil {
		t.Fatal("publication staging was not on the video volume", err)
	}
	if err := os.Remove(stageProbe); err != nil {
		t.Fatal(err)
	}
	const imported = "separate cross-volume import content"
	source := filepath.Join(t.TempDir(), "source.mp4")
	if err := os.WriteFile(source, []byte(imported), 0600); err != nil {
		t.Fatal(err)
	}
	receipt, err := engine.ImportVideo(t.Context(), source, map[string]bool{videoKey(imported): true})
	if err != nil || receipt == nil {
		t.Fatal("cross-volume import", receipt, err)
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	importedInfo, err := os.Stat(receipt.Destination)
	if err != nil || os.SameFile(sourceInfo, importedInfo) {
		t.Fatal("cross-volume import was not an independent copy", err)
	}
	if got, err := os.ReadFile(receipt.Destination); err != nil || string(got) != imported {
		t.Fatal("imported bytes changed", err)
	}
	entries, err := os.ReadDir(cfg.publishDir())
	if err != nil || len(entries) != 0 {
		t.Fatal("publication or import staging leaked", entries, err)
	}
	t.Logf("database=%s blocks=%s videos=%s; distinct volumes verified by ERROR_NOT_SAME_DEVICE", cfg.Store.path, cfg.Work.Path(), cfg.VideoDir)
}
