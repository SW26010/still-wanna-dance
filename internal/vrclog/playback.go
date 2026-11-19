package vrclog

import (
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// PlaybackEvent contains selected formal-player facts, never raw lines,
// player names, world IDs or signed media URLs. At is the log event time,
// not the time the tailer happened to read the line (second precision).
type PlaybackEvent struct {
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	SongID   int64     `json:"songID,omitempty"`
	Owner    *bool     `json:"owner,omitempty"`
	Position float64   `json:"position,omitempty"`
	Playing  *bool     `json:"playing,omitempty"`
}

var syncPosition = regexp.MustCompile(`Syncing video to ([0-9]+(?:\.[0-9]+)?)(?:,|\s|$)`)
var localRole = regexp.MustCompile(`isLocal = True, isOwner = (True|False),`)
var ownerPlaying = regexp.MustCompile(`ownerPlaying = (True|False),`)

// ParseWithPlayback adds observed WannaDance formal-player events to the
// existing queue/lifecycle parser. Unknown worlds and previews are ignored.
// This is opt-in: Parse and ordinary Tail users retain queue-only behavior.
func ParseWithPlayback(line string) (Event, bool, error) {
	if event, ok, err := Parse(line); ok || err != nil {
		return event, ok, err
	}
	return parsePlayerFact(line)
}

func parsePlayer(line string) (Event, bool, error) {
	if _, queue := queueSnapshotPayload(line); queue {
		return Event{}, false, nil
	}
	if roomReset(line) {
		return Event{Reset: true}, true, nil
	}
	return parsePlayerFact(line)
}

func parsePlayerFact(line string) (Event, bool, error) {
	if len(line) < 19 {
		return Event{}, false, nil
	}
	at, err := time.ParseInLocation("2006.01.02 15:04:05", line[:19], time.Local)
	if err != nil {
		return Event{}, false, nil
	}
	e := &PlaybackEvent{At: at}
	queue := strings.Contains(line, "[VideoQueueManager]")
	formal := strings.Contains(line, "USharpVideo (WannaDance)")
	switch {
	case queue && strings.Contains(line, "[LogOtherPlayerAndDeviceType]"):
		m := localRole.FindStringSubmatch(line)
		if m == nil {
			return Event{}, false, nil
		}
		owner := m[1] == "True"
		e.Kind, e.Owner = "role", &owner
	case queue && strings.Contains(line, "OnUSharpVideoDeserialization:"):
		m := ownerPlaying.FindStringSubmatch(line)
		if m == nil {
			return Event{}, false, nil
		}
		playing := m[1] == "True"
		e.Kind, e.Playing = "transport", &playing
	case formal:
		switch {
		case strings.Contains(line, "LoadRoutedURL:") || strings.Contains(line, "Playing synced "):
			e.Kind = "route"
		case strings.Contains(line, "Started video:"):
			e.Kind = "start"
			if strings.Contains(line, "since I'm the owner") {
				owner := true
				e.Owner = &owner
			} else if strings.Contains(line, "since owner is playing") {
				owner := false
				e.Owner = &owner
			}
		case strings.Contains(line, "Paused video:"):
			e.Kind = "pause"
		case strings.Contains(line, "OnVideoEnd:"):
			e.Kind = "end"
		case strings.Contains(line, "StopVideo"):
			e.Kind = "stop"
		case strings.Contains(line, "OnVideoError:"):
			e.Kind = "error"
		case strings.Contains(line, "Retrying load:"):
			e.Kind = "reload"
		default:
			m := syncPosition.FindStringSubmatch(line)
			if m == nil {
				return Event{}, false, nil
			}
			position, err := strconv.ParseFloat(m[1], 64)
			if err != nil || math.IsNaN(position) || math.IsInf(position, 0) {
				return Event{}, false, nil
			}
			e.Kind, e.Position = "sync", position
		}
		e.SongID = playbackSong(line)
	default:
		return Event{}, false, nil
	}
	return Event{Playback: e}, true, nil
}

func playbackSong(line string) int64 {
	for _, word := range strings.Fields(line) {
		if !strings.HasPrefix(word, "http://") && !strings.HasPrefix(word, "https://") {
			continue
		}
		u, err := url.Parse(strings.TrimRight(word, ",;"))
		if err != nil {
			continue
		}
		if u.Hostname() != "api.udon.dance" || u.Path != "/Api/Songs/play" {
			continue
		}
		id, err := strconv.ParseInt(u.Query().Get("id"), 10, 64)
		if err == nil && id > 0 {
			return id
		}
	}
	return 0
}
