package applog

import (
	"bytes"
	"errors"
	"io"
)

// Keep the standard handler's value encoding, groups and shared write lock.
// Only its three controlled built-in prefixes are changed here.
type textWriter struct{ output io.Writer }

func (w textWriter) Write(p []byte) (int, error) {
	original := len(p)
	line := make([]byte, 0, original)
	if bytes.HasPrefix(p, []byte("time=")) {
		stamp, rest, ok := bytes.Cut(p[len("time="):], []byte(" "))
		if !ok {
			return 0, errors.New("invalid text log timestamp")
		}
		line = append(line, stamp...)
		line = append(line, ' ')
		p = rest
	}
	if !bytes.HasPrefix(p, []byte("level=")) {
		return 0, errors.New("invalid text log level")
	}
	level, body, ok := bytes.Cut(p[len("level="):], []byte(" msg="))
	if !ok {
		return 0, errors.New("invalid text log message")
	}
	line = append(line, '[')
	line = append(line, level...)
	line = append(line, ']', ' ')
	line = append(line, body...)
	n, err := w.output.Write(line)
	if err == nil && n != len(line) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return 0, err
	}
	return original, nil
}
