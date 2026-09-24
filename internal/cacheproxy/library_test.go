package cacheproxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestExistingLibraryMetadataVariantsAndNoStateDependency(t *testing.T) {
	for _, meta := range []string{
		`{"id":1344,"title":"full title","custom":{"keep":true},"url":"original","checksum":"old"}`,
		`{"id":1344,"checksum":"old"}`,
		`broken json`,
		"", // missing metadata: independently verified video is safe to read.
	} {
		t.Run(fmt.Sprint(len(meta)), func(t *testing.T) {
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected download") })
			dir := filepath.Join(cfg.SongsDir, "1344")
			writeTestFile(t, filepath.Join(dir, "video.mp4"), payload)
			if meta != "" {
				writeTestFile(t, filepath.Join(dir, "metadata.json"), meta)
			}
			writeTestFile(t, filepath.Join(dir, "download.txt"), "preserve")
			writeTestFile(t, filepath.Join(cfg.SongsDir, "failed.txt"), "root auxiliary")
			for i := 0; i < 2; i++ {
				assertResponse(t, request(s, "GET", videoURL(payload), map[string]string{"Range": "bytes=10-13"}), 206, "abcd")
				s.Close()
				cfg.CacheDir = t.TempDir() // No retained application state or import step.
				var err error
				s, err = New(cfg)
				if err != nil {
					t.Fatal(err)
				}
			}
			defer s.Close()
			for name, expected := range map[string]string{"video.mp4": payload, "download.txt": "preserve", "metadata.json": meta} {
				got, err := os.ReadFile(filepath.Join(dir, name))
				if name == "metadata.json" && meta == "" {
					if !os.IsNotExist(err) {
						t.Fatal("metadata created on hit")
					}
					continue
				}
				if err != nil || string(got) != expected {
					t.Fatalf("%s changed: %v", name, err)
				}
			}
			files, _ := os.ReadDir(cfg.CacheDir)
			if len(files) != 1 || files[0].Name() != ".lock" {
				t.Fatal("hit unexpectedly copied video into state", files)
			}
			root, _ := os.ReadFile(filepath.Join(cfg.SongsDir, "failed.txt"))
			if string(root) != "root auxiliary" {
				t.Fatal("root auxiliary changed")
			}
		})
	}
}

func TestLibraryUpdatePreservesMetadataAndFailedDownloadKeepsOld(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) {
				if fail {
					io.WriteString(w, "bad")
					return
				}
				io.WriteString(w, payload)
			})
			dir := filepath.Join(cfg.SongsDir, "1344")
			old := "old version"
			metadata := `{"id":1344,"title":"my song","volume":0.8,"custom":{"keep":true},"checksum":"old"}`
			writeTestFile(t, filepath.Join(dir, "video.mp4"), old)
			writeTestFile(t, filepath.Join(dir, "metadata.json"), metadata)
			writeTestFile(t, filepath.Join(dir, "download.txt"), "keep")
			w := request(s, "GET", videoURL(payload), nil)
			got, err := os.ReadFile(filepath.Join(dir, "video.mp4"))
			if err != nil {
				t.Fatal(err)
			}
			meta, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
			if err != nil {
				t.Fatal(err)
			}
			if fail {
				if w.Code != 502 || string(got) != old || string(meta) != metadata {
					t.Fatal("failed update damaged existing files")
				}
				assertResponse(t, request(s, "GET", videoURL(old), nil), 200, old)
			} else {
				assertResponse(t, w, 200, payload)
				if string(got) != payload {
					t.Fatal("new video not published")
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(meta, &fields); err != nil {
					t.Fatal(err)
				}
				if string(fields["title"]) != `"my song"` || string(fields["volume"]) != "0.8" || len(fields["custom"]) == 0 || string(fields["checksum"]) == `"old"` {
					t.Fatal(string(meta))
				}
			}
			aux, _ := os.ReadFile(filepath.Join(dir, "download.txt"))
			if string(aux) != "keep" {
				t.Fatal("auxiliary lost")
			}
		})
	}
}

func TestActiveReaderNeverSeesMixedVersions(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	path := filepath.Join(cfg.SongsDir, "1344", "video.mp4")
	writeTestFile(t, path, "old version")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := request(s, "GET", videoURL(payload), nil)
	if runtime.GOOS == "windows" && (w.Code == 502 || w.aborted) {
		// Windows may block replacement while a reader holds the target open.
		// The validated download stays in application state for the next retry.
	} else {
		assertResponse(t, w, 200, payload)
	}
	old, err := io.ReadAll(f)
	if err != nil || string(old) != "old version" {
		t.Fatal("opened version changed", string(old), err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
	newVideo, err := os.ReadFile(path)
	if err != nil || string(newVideo) != payload {
		t.Fatal("new version missing", err)
	}
}

func TestInvalidMetadataBlocksReplacementWithoutClobbering(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) })
	dir := filepath.Join(cfg.SongsDir, "1344")
	writeTestFile(t, filepath.Join(dir, "video.mp4"), "old")
	writeTestFile(t, filepath.Join(dir, "metadata.json"), "broken")
	w := request(s, "GET", videoURL(payload), nil)
	if w.Code != 502 && !w.aborted {
		t.Fatal(w.Code)
	}
	video, _ := os.ReadFile(filepath.Join(dir, "video.mp4"))
	meta, _ := os.ReadFile(filepath.Join(dir, "metadata.json"))
	if string(video) != "old" || string(meta) != "broken" {
		t.Fatal("damaged existing files")
	}
	assertNoPartial(t, dir)
}
