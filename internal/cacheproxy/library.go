package cacheproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Video bytes are authoritative. Metadata is diagnosed without modifying an
// existing library during a hit; missing optional fields never force a download.
func (s *Server) inspectMetadata(v video) {
	data, err := os.ReadFile(filepath.Join(s.cfg.SongsDir, v.id, "metadata.json"))
	var meta map[string]json.RawMessage
	if err == nil {
		err = json.Unmarshal(data, &meta)
	}
	if err == nil {
		var checksum string
		if err = json.Unmarshal(meta["checksum"], &checksum); err == nil && checksum != v.checksum {
			err = errors.New("metadata checksum differs from verified video")
		}
	}
	if err != nil {
		s.cfg.Logger.Warn("metadata_warning", "song_id", v.id, "error", err)
	}
}

// publishLibrary keeps the original metadata's unknown fields. Only complete
// validated video files are renamed into place within the destination directory.
// Video+metadata are not a two-file transaction (see docs/mvp.md).
func (s *Server) publishLibrary(ctx context.Context, source string, v video) error {
	s.libraryMu.Lock()
	defer s.libraryMu.Unlock()
	dir := filepath.Join(s.cfg.SongsDir, v.id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	metadataPath := filepath.Join(dir, "metadata.json")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".stepstash-") && strings.HasSuffix(entry.Name(), ".part") {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				return err
			}
		}
	}
	meta := map[string]json.RawMessage{}
	data, err := os.ReadFile(metadataPath)
	if err == nil {
		if err := json.Unmarshal(data, &meta); err != nil || meta == nil {
			return errors.New("existing metadata is invalid; refusing to overwrite it")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		// Matches the simplified shape observed from original proxy downloads.
		data = []byte(`{"category":114514,"categoryName":"","titleSpell":"","playerIndex":0,"volume":0,"start":0,"end":0,"flip":false,"skipRandom":false,"originalUrl":null}`)
		if err := json.Unmarshal(data, &meta); err != nil {
			return err
		}
		meta["title"], _ = json.Marshal(v.id)
	} else {
		return err
	}
	meta["id"] = json.RawMessage(v.id)
	meta["checksum"], _ = json.Marshal(v.checksum)
	data, err = json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	metadataTemp, err := os.CreateTemp(dir, ".stepstash-metadata-*.part")
	if err != nil {
		return err
	}
	defer os.Remove(metadataTemp.Name())
	defer metadataTemp.Close()
	if _, err := metadataTemp.Write(data); err != nil {
		return err
	}
	if err := metadataTemp.Sync(); err != nil {
		return err
	}
	if err := metadataTemp.Close(); err != nil {
		return err
	}
	videoTemp, err := os.CreateTemp(dir, ".stepstash-video-*.part")
	if err != nil {
		return err
	}
	defer os.Remove(videoTemp.Name())
	defer videoTemp.Close()
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	if _, err := io.Copy(videoTemp, contextReader{ctx, src}); err != nil {
		return err
	}
	if err := videoTemp.Sync(); err != nil {
		return err
	}
	if err := videoTemp.Close(); err != nil {
		return err
	}
	if err := checkFile(ctx, videoTemp.Name(), v); err != nil {
		return err
	}
	if err := os.Rename(videoTemp.Name(), filepath.Join(dir, "video.mp4")); err != nil {
		return fmt.Errorf("publish library video: %w", err)
	}
	if err := os.Rename(metadataTemp.Name(), metadataPath); err != nil {
		return fmt.Errorf("video published but metadata update failed: %w", err)
	}
	return nil
}
