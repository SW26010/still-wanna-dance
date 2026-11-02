package applog

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

const segmentLayout = "2006-01-02T15-04-05.000000000Z.log"

type segment struct {
	name string
	info os.FileInfo
}

func validateSegment(file segment) error {
	limit := MaxBytes
	if legacyOrder(file.name) != 0 {
		limit = 5 << 20
	}
	if !file.info.Mode().IsRegular() || file.info.Size() > limit {
		return fmt.Errorf("invalid log file %s", file.name)
	}
	return nil
}

func segmentTime(name string) (time.Time, bool) {
	at, err := time.Parse(segmentLayout, name)
	return at, err == nil && at.Format(segmentLayout) == name
}

// Legacy logs remain readable and participate in retention without migration.
func legacyOrder(name string) int {
	switch name {
	case "app.log.3":
		return 1
	case "app.log.2":
		return 2
	case "app.log.1":
		return 3
	case "app.log":
		return 4
	}
	return 0
}

// logSegments is the single ownership and ordering rule for writers and readers.
// Unknown names are never read or removed. Old numbered logs precede new segments.
func logSegments(root *os.Root) ([]segment, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	var files []segment
	for _, entry := range entries {
		name := entry.Name()
		_, current := segmentTime(name)
		legacy := legacyOrder(name) != 0
		if !current && !legacy {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		file := segment{name: name, info: info}
		if err := validateSegment(file); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	slices.SortFunc(files, func(a, b segment) int {
		x, y := legacyOrder(a.name), legacyOrder(b.name)
		if x != 0 || y != 0 {
			if x == 0 {
				return 1
			}
			if y == 0 {
				return -1
			}
			return x - y
		}
		return strings.Compare(a.name, b.name)
	})
	return files, nil
}
