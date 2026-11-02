package applog

import (
	"bufio"
	"bytes"
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
)

// ReadLimit bounds both the response and the reader's retained records.
const ReadLimit = 500

type Query struct {
	From, To time.Time
	Level    string
	Event    string
}

func (q Query) Validate() error {
	if !q.From.IsZero() && !q.To.IsZero() && !q.To.After(q.From) {
		return errors.New("结束时间必须晚于开始时间")
	}
	switch q.Level {
	case "", "DEBUG", "INFO", "WARN", "ERROR":
		return nil
	default:
		return errors.New("日志级别必须为 DEBUG、INFO、WARN 或 ERROR")
	}
}

type Entry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	Fields  []Field   `json:"fields"`
	File    string    `json:"file"`
	Line    int       `json:"line"`
	Raw     string    `json:"raw"`
}

type Field struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type ReadResult struct {
	Entries []Entry  `json:"entries"`
	Files   []string `json:"files"`
	Matches int      `json:"matches"`
	Skipped int      `json:"skipped"`
	Limit   int      `json:"limit"`
}

type logFile struct {
	file *os.File
	segment
}

var errRotated = errors.New("log files rotated during snapshot")

// openSnapshot captures handles and lengths, then checks their names once more.
// Appends cannot extend this read; later renames cannot change the captured files.
func openSnapshot(root *os.Root) (files []logFile, resultErr error) {
	defer func() {
		if resultErr != nil {
			closeLogs(files)
		}
	}()
	segments, err := logSegments(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errRotated
	}
	if err != nil {
		return nil, err
	}
	var total int64
	// Directory entries discover names; their file IDs are unavailable on some
	// Windows volumes. Compare handle stats with root.Stat below instead.
	for _, item := range segments {
		f, err := root.Open(item.name)
		if errors.Is(err, os.ErrNotExist) {
			return files, errRotated
		}
		if err != nil {
			return files, err
		}
		info, err := f.Stat()
		files = append(files, logFile{file: f, segment: segment{name: item.name, info: info}})
		if err != nil {
			return files, err
		}
		if err := validateSegment(files[len(files)-1].segment); err != nil {
			return files, err
		}
		total += info.Size()
		if total > MaxTotalBytes {
			return files, validateSnapshot(root, files, total)
		}
	}
	return files, validateSnapshot(root, files, total)
}

func validateSnapshot(root *os.Root, files []logFile, total int64) error {
	// A captured total can include already-deleted segments. Recheck identity
	// first so rotation uses the existing retry instead of reporting oversize.
	for _, file := range files {
		info, err := root.Stat(file.name)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err != nil || !os.SameFile(file.info, info) {
			return errRotated
		}
	}
	if total > MaxTotalBytes {
		return errors.New("log snapshot exceeds size limit")
	}
	return nil
}

func closeLogs(files []logFile) {
	for _, file := range files {
		if file.file != nil {
			_ = file.file.Close()
		}
	}
}

type candidate struct {
	Entry
	sequence int
}

func older(a, b candidate) bool {
	if a.Time.Equal(b.Time) {
		return a.sequence < b.sequence
	}
	return a.Time.Before(b.Time)
}

type recentEntries []candidate

func (h recentEntries) Len() int           { return len(h) }
func (h recentEntries) Less(i, j int) bool { return older(h[i], h[j]) }
func (h recentEntries) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *recentEntries) Push(v any)        { *h = append(*h, v.(candidate)) }
func (h *recentEntries) Pop() any {
	old := *h
	v := old[len(old)-1]
	*h = old[:len(old)-1]
	return v
}

// Read queries only this application's fixed, bounded log set. It creates no
// directories or indexes and never borrows the writer or resource engine.
func Read(ctx context.Context, dir string, q Query) (ReadResult, error) {
	result, err := read(ctx, dir, q, ReadLimit)
	if err != nil {
		return result, err
	}
	for i := range result.Entries {
		result.Entries[i].Fields = readableFields(result.Entries[i].Raw)
	}
	return result, nil
}

func read(ctx context.Context, dir string, q Query, limit int) (ReadResult, error) {
	result := ReadResult{Entries: []Entry{}, Files: []string{}, Limit: limit}
	if err := q.Validate(); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	root, err := os.OpenRoot(dir)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer root.Close()
	files, err := openSnapshot(root)
	if errors.Is(err, errRotated) {
		files, err = openSnapshot(root)
	}
	if err != nil {
		return result, err
	}
	defer closeLogs(files)
	var recent recentEntries
	keyword := strings.ToLower(strings.TrimSpace(q.Event))
	sequence := 0
	for _, file := range files {
		result.Files = append(result.Files, file.name)
		reader := bufio.NewReader(io.NewSectionReader(file.file, 0, file.info.Size()))
		for line := 1; ; line++ {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			raw, readErr := reader.ReadBytes('\n')
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return result, readErr
			}
			if raw = bytes.TrimSpace(raw); len(raw) > 0 {
				var record struct {
					Time    time.Time `json:"time"`
					Level   string    `json:"level"`
					Message *string   `json:"msg"`
				}
				if err := json.Unmarshal(raw, &record); err != nil || record.Time.IsZero() || record.Level == "" || record.Message == nil {
					// A crash or an in-progress write may leave an incomplete line.
					// Report it without altering the original diagnostic evidence.
					result.Skipped++
				} else {
					if (q.From.IsZero() || !record.Time.Before(q.From)) && (q.To.IsZero() || record.Time.Before(q.To)) && (q.Level == "" || record.Level == q.Level) && (keyword == "" || strings.Contains(strings.ToLower(*record.Message), keyword)) {
						result.Matches++
						sequence++
						entry := candidate{Entry: Entry{Time: record.Time, Level: record.Level, Message: *record.Message, File: file.name, Line: line, Raw: string(raw)}, sequence: sequence}
						if limit == 0 {
							recent = append(recent, entry)
						} else if len(recent) < limit {
							heap.Push(&recent, entry)
						} else if older(recent[0], entry) {
							recent[0] = entry
							heap.Fix(&recent, 0)
						}
					}
				}
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
		}
	}
	slices.SortFunc(recent, func(a, b candidate) int {
		if older(a, b) {
			return 1
		}
		if older(b, a) {
			return -1
		}
		return 0
	})
	for _, entry := range recent {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Entries = append(result.Entries, entry.Entry)
	}
	return result, nil
}

type ExportResult struct {
	Data             []byte
	Matches, Skipped int
}

// Export uses the same bounded file snapshot, filters and ordering as Read,
// without the display limit. It never writes a temporary file on the server.
func Export(ctx context.Context, dir string, q Query) (ExportResult, error) {
	result, err := read(ctx, dir, q, 0)
	if err != nil {
		return ExportResult{}, err
	}
	var output bytes.Buffer
	for _, entry := range result.Entries {
		if err := ctx.Err(); err != nil {
			return ExportResult{}, err
		}
		fmt.Fprintf(&output, "%s [%s] %s\n", entry.Time.Format(time.RFC3339Nano), entry.Level, entry.Message)
		for _, field := range readableFields(entry.Raw) {
			fmt.Fprintf(&output, "%s=%s\n", field.Key, field.Value)
		}
		fmt.Fprintf(&output, "source=%s:%d\n\n", entry.File, entry.Line)
	}
	return ExportResult{Data: output.Bytes(), Matches: result.Matches, Skipped: result.Skipped}, nil
}

func readableFields(raw string) []Field {
	// RawMessage preserves numeric precision and nested JSON. Only strings are
	// unquoted for reading; the original record remains available alongside it.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal([]byte(raw), &fields)
	delete(fields, "time")
	delete(fields, "level")
	delete(fields, "msg")
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	readable := make([]Field, 0, len(keys))
	for _, key := range keys {
		value := string(fields[key])
		var text string
		if json.Unmarshal(fields[key], &text) == nil && value != "null" {
			value = text
		}
		readable = append(readable, Field{Key: key, Value: value})
	}
	return readable
}
