//go:build ignore

// Run with explicit inputs; never fetches catalogs or stores signed URLs.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	mediaseed "still-wanna-dance/data/media-seed"
	"still-wanna-dance/internal/catalogmeta"
)

func main() {
	version := flag.String("version", "", "distribution snapshot version (YYYYMMDDhhmmss)")
	out := flag.String("out", "data/media-seed/media-seed.json", "output file")
	reference := flag.String("reference", "", "optional sanitized local snapshot from export-media-seed.py")
	flag.Parse()
	if err := generate(*version, *out, *reference, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(version, out, reference string, inputs []string) error {
	if _, err := catalogmeta.ParseTime(version); err != nil {
		return err
	}
	if len(inputs) == 0 {
		return fmt.Errorf("provide the saved catalog input files")
	}
	type candidate struct {
		catalog catalogmeta.Catalog
		source  mediaseed.Source
	}
	var candidates []candidate
	for _, path := range inputs {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		c, err := catalogmeta.Parse(body)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		hash := sha256.Sum256(body)
		candidates = append(candidates, candidate{c, mediaseed.Source{Name: filepath.Base(path), Revision: c.Revision, SHA256: hex.EncodeToString(hash[:])}})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].catalog.Revision != candidates[j].catalog.Revision {
			return candidates[i].catalog.Revision > candidates[j].catalog.Revision
		}
		return candidates[i].source.Name < candidates[j].source.Name
	})
	seed := mediaseed.Snapshot{Version: version}
	media := map[string]*int64{}
	versions := map[int64]string{}
	mappings := map[int64]string{}
	for _, v := range candidates {
		seed.Sources = append(seed.Sources, v.source)
		if v.catalog.Udon {
			continue
		}
		for _, song := range v.catalog.Entries {
			media[song.Checksum] = nil
			if previous, ok := mappings[song.ID]; ok {
				if versions[song.ID] == v.catalog.Revision && previous != song.Checksum {
					return fmt.Errorf("same-version MD5 conflict for song %d", song.ID)
				}
				continue
			}
			mappings[song.ID], versions[song.ID] = song.Checksum, v.catalog.Revision
		}
	}
	if reference != "" {
		body, err := os.ReadFile(reference)
		if err != nil {
			return err
		}
		var snapshot mediaseed.Snapshot
		decoder := json.NewDecoder(strings.NewReader(string(body)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&snapshot); err != nil {
			return err
		}
		if _, err := catalogmeta.ParseTime(snapshot.Version); err != nil {
			return err
		}
		for _, resource := range snapshot.Media {
			decoded, err := hex.DecodeString(resource.MD5)
			if err != nil || len(decoded) != 16 || strings.ToLower(resource.MD5) != resource.MD5 || (resource.ByteSize != nil && *resource.ByteSize <= 0) {
				return fmt.Errorf("invalid reference media")
			}
			media[resource.MD5] = resource.ByteSize
		}
		for _, mapping := range snapshot.SongMedia {
			if _, exists := media[mapping.MD5]; !exists || mapping.ID <= 0 {
				return fmt.Errorf("invalid reference mapping")
			}
			mappings[mapping.ID] = mapping.MD5
		}
		hash := sha256.Sum256(body)
		seed.Sources = append(seed.Sources, mediaseed.Source{Name: "local-media-projection", Revision: snapshot.Version, SHA256: hex.EncodeToString(hash[:])})
	}
	if len(media) == 0 {
		return fmt.Errorf("no MD5 resources in inputs")
	}
	for md5, size := range media {
		seed.Media = append(seed.Media, mediaseed.Media{MD5: md5, ByteSize: size})
	}
	sort.Slice(seed.Media, func(i, j int) bool { return seed.Media[i].MD5 < seed.Media[j].MD5 })
	for id, md5 := range mappings {
		seed.SongMedia = append(seed.SongMedia, mediaseed.Mapping{ID: id, MD5: md5})
	}
	sort.Slice(seed.SongMedia, func(i, j int) bool { return seed.SongMedia[i].ID < seed.SongMedia[j].ID })
	body, err := json.MarshalIndent(seed, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, append(body, '\n'), 0600); err != nil {
		return err
	}
	fmt.Printf("seed %s: %d media, %d song mappings\n", version, len(seed.Media), len(seed.SongMedia))
	return nil
}
