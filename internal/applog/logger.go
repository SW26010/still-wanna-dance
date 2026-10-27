package applog

import (
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"os"
)

type Level int

const (
	Debug Level = Level(slog.LevelDebug)
	Info  Level = Level(slog.LevelInfo)
	Warn  Level = Level(slog.LevelWarn)
	Error Level = Level(slog.LevelError)
	Off   Level = 100
)

func ParseLevel(value string) (Level, error) {
	switch value {
	case "", "info":
		return Info, nil
	case "debug":
		return Debug, nil
	case "warn":
		return Warn, nil
	case "error":
		return Error, nil
	case "off":
		return Off, nil
	default:
		return Info, errors.New("程序日志级别必须为 off、error、warn、info 或 debug")
	}
}

func (l Level) Level() slog.Level { return slog.Level(l) }

func (l Level) String() string {
	if l == Off {
		return "off"
	}
	switch l {
	case Debug:
		return "debug"
	case Warn:
		return "warn"
	case Error:
		return "error"
	default:
		return "info"
	}
}

// New gives both entrypoints the same format and process-session identity.
// Off disables the handler itself, so it neither formats nor writes records.
func New(output io.Writer, level Level) *slog.Logger {
	if level == Off {
		return slog.New(slog.DiscardHandler)
	}
	return slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Value.Kind() == slog.KindTime {
				a.Value = slog.TimeValue(a.Value.Time().UTC())
			}
			return a
		},
	})).With("pid", os.Getpid(), "session_id", rand.Text())
}
