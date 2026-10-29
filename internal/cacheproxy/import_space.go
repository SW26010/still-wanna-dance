package cacheproxy

import "os"

// EstimateImportSpace only reads the two directories, without enumerating or
// hashing videos. ImportSnapshotVideo still decides per file by attempting a link.
func EstimateImportSpace(source, destination string) string {
	volume := func(path string) (uint64, error) {
		f, err := os.Open(path)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return 0, err
		}
		if !info.IsDir() {
			return 0, os.ErrInvalid
		}
		return fileVolumeID(f)
	}
	from, err := volume(source)
	if err != nil {
		return "unknown"
	}
	to, err := volume(destination)
	if err != nil {
		return "unknown"
	}
	if from == to {
		return "same-volume"
	}
	return "cross-volume"
}
