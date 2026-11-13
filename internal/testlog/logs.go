// Package testlog reads diagnostic records for integration tests, not the app.
package testlog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Entry struct {
	Raw    string
	Fields map[string]any
}

type Snapshot struct {
	Files   []string
	Entries []Entry
}

func EventName(record slog.Record) string {
	var name string
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "event_name" {
			name = attr.Value.String()
			return false
		}
		return true
	})
	return name
}

// Parse preserves the existing JSON capture tests and reads the new text form.
func Parse(line []byte) (map[string]any, error) {
	line = bytes.TrimSpace(line)
	fields := map[string]any{}
	if len(line) > 0 && line[0] == '{' {
		decoder := json.NewDecoder(bytes.NewReader(line))
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key := token.(string)
			if _, exists := fields[key]; exists {
				return nil, fmt.Errorf("duplicate log field %q", key)
			}
			var value any
			if err := decoder.Decode(&value); err != nil {
				return nil, err
			}
			fields[key] = value
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		if fields["event_name"] == nil {
			fields["event_name"] = fields["msg"]
		}
		return fields, nil
	}
	text := string(line)
	for _, key := range []string{"time", "level", "msg"} {
		token, rest, err := next(text)
		if err != nil {
			return nil, err
		}
		text = rest
		if key == "level" {
			token = strings.Trim(token, "[]")
		}
		if key == "msg" && strings.HasPrefix(token, `"`) {
			token, err = strconv.Unquote(token)
			if err != nil {
				return nil, err
			}
		}
		fields[key] = token
	}
	for text != "" {
		token, rest, err := next(text)
		if err != nil {
			return nil, err
		}
		text = rest
		quoted, split := false, -1
		for i := 0; i < len(token); i++ {
			if token[i] == '\\' && quoted {
				i++
			} else if token[i] == '"' {
				quoted = !quoted
			} else if token[i] == '=' && !quoted {
				split = i
				break
			}
		}
		if split <= 0 {
			return nil, fmt.Errorf("invalid log field %q", token)
		}
		key, value := token[:split], token[split+1:]
		if strings.HasPrefix(key, `"`) {
			key, err = strconv.Unquote(key)
			if err != nil {
				return nil, err
			}
		}
		var decoded any = value
		if strings.HasPrefix(value, `"`) {
			decoded, err = strconv.Unquote(value)
			if err != nil {
				return nil, err
			}
		} else if value == "true" || value == "false" {
			decoded = value == "true"
		} else if number, err := strconv.ParseFloat(value, 64); err == nil {
			decoded = number
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate log field %q", key)
		}
		fields[key] = decoded
	}
	return fields, nil
}

func next(text string) (string, string, error) {
	text = strings.TrimSpace(text)
	quoted := false
	for i := 0; i < len(text); i++ {
		if text[i] == '\\' && quoted {
			i++
		} else if text[i] == '"' {
			quoted = !quoted
		} else if text[i] == ' ' && !quoted {
			return text[:i], strings.TrimSpace(text[i+1:]), nil
		}
	}
	if text == "" || quoted {
		return "", "", fmt.Errorf("incomplete log token %q", text)
	}
	return text, "", nil
}

var managedName = regexp.MustCompile(`^(?:[0-9]{8}(?:T[0-9]{6}Z)?(?:-[1-9][0-9]*)?\.log|app\.log(?:\.[1-3])?)$`)

func Read(dir string) (Snapshot, error) {
	var result Snapshot
	entries, err := os.ReadDir(dir)
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() && managedName.MatchString(entry.Name()) {
			result.Files = append(result.Files, entry.Name())
		}
	}
	// Old numbered logs are first; record ordering is immaterial to these tests.
	sort.Slice(result.Files, func(i, j int) bool {
		a, b := result.Files[i], result.Files[j]
		if strings.HasPrefix(a, "app.log") != strings.HasPrefix(b, "app.log") {
			return strings.HasPrefix(a, "app.log")
		}
		if strings.HasPrefix(a, "app.log") {
			return a > b
		}
		return a < b
	})
	for _, name := range result.Files {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return result, err
		}
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			fields, err := Parse(line)
			if err != nil {
				return result, fmt.Errorf("%s: %w", name, err)
			}
			result.Entries = append(result.Entries, Entry{string(line), fields})
		}
	}
	return result, nil
}
