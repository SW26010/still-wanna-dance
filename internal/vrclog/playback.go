package vrclog

import (
	"encoding/json"
	"hash/maphash"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// PlaybackEvent contains selected player and room facts, never raw lines,
// player names, world IDs or signed media URLs. At is the log event time,
// not the time the tailer happened to read the line (second precision).
type PlaybackEvent struct {
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	SongID   int64     `json:"songID,omitempty"`
	external uint64    // Process-local identity. Never retain or serialize the URL.
	Owner    *bool     `json:"owner,omitempty"`
	Position float64   `json:"position,omitempty"`
	Playing  *bool     `json:"playing,omitempty"`
}

var roomEndAction = regexp.MustCompile(`\[VideoQueueManager\]\s+(?:\[\d{2}:\d{2}:\d{2}\]\s*:\s*)?` +
	`(?:OnUSharpVideoEnd:\s+PlayNextVideo\(\)|OnUSharpVideoStop:\s+I am owner,\s+QueueChanged\(\))(?:\s|<|$)`)
var roomStopButton = regexp.MustCompile(`\[VideoQueueManager\]\s+(?:\[\d{2}:\d{2}:\d{2}\]\s*:\s*)?StopVideo Button Clicked(?:\s|<|$)`)
var roomSelectionData = regexp.MustCompile(`\[VideoQueueManager\]\s+(?:\[\d{2}:\d{2}:\d{2}\]\s*:\s*)?(?:PlayQueueVideo:(?:\s*index\s*=\s*(\d+)\s*,)?|PlayRandomVideo:).*?\buserData\s*=`)
var previewSong = regexp.MustCompile(`\[VideoListManager\]\s+PreviewVideo:\s+(\d+)(?:\s|$)`)
var playerPosition = regexp.MustCompile(`(Syncing video to|SeekTo: call SyncVideo\(\) since newTargetTime is) ([0-9]+(?:\.[0-9]+)?)(?:,|\s|$)`)
var syncThreshold = regexp.MustCompile(`\bthreshold\s*=\s*(0\.4999999|0\.5|0\.85)(?:\s|<|$)`)
var localRole = regexp.MustCompile(`isLocal = True, isOwner = (True|False),`)
var ownerPlaying = regexp.MustCompile(`ownerPlaying = (True|False),`)

// ParseWithPlayback recognizes queue, room lifecycle and formal-player events.
// Preview markers let the timeline exclude callbacks from the local preview player.
func ParseWithPlayback(line string) (Event, bool, error) {
	if event, ok, err := Parse(line); ok || err != nil {
		if event.Songs != nil {
			event.queueAt = logTime(line)
		}
		return event, ok, err
	}
	return parsePlayerFact(line)
}

func logTime(line string) time.Time {
	if len(line) < 19 {
		return time.Time{}
	}
	at, _ := time.ParseInLocation("2006.01.02 15:04:05", line[:19], time.Local)
	return at
}

func parsePlayerFact(line string) (Event, bool, error) {
	at := logTime(line)
	if at.IsZero() {
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
	case queue:
		if roomStopButton.MatchString(line) {
			e.Kind = "room-stop-request"
			break
		}
		if roomEndAction.MatchString(line) {
			e.Kind = "room-end"
			if strings.Contains(line, "OnUSharpVideoStop:") {
				e.Kind = "room-stop"
			}
			break
		}
		match := roomSelectionData.FindStringSubmatchIndex(line)
		var song Song
		if match == nil || json.NewDecoder(strings.NewReader(line[match[1]:])).Decode(&song) != nil || song.ID == 0 || song.ID < -1 {
			return Event{}, false, nil
		}
		e.Kind, e.SongID = "room-selection", song.ID
		if song.ID == -1 && (match[2] < 0 || line[match[2]:match[3]] != "0") {
			e.SongID = 0 // An external choice, but no identifiable queue head.
		}
	case strings.Contains(line, "[VideoListManager]"):
		m := previewSong.FindStringSubmatch(line)
		if m == nil {
			return Event{}, false, nil
		}
		id, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || id <= 0 {
			return Event{}, false, nil
		}
		e.Kind, e.SongID = "preview", id
	case formal:
		switch {
		case strings.Contains(line, "Playing synced "):
			e.Kind = "room-route"
		case strings.Contains(line, "LoadRoutedURL:"):
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
			if strings.Contains(line, " ended, but I am not the video owner, waiting for video owner ") {
				e.Kind = "room-end"
			}
		case strings.Contains(line, "StopVideo"):
			e.Kind = "stop"
		case strings.Contains(line, "OnVideoError:"):
			e.Kind = "error"
		case strings.Contains(line, "Retrying load:"):
			e.Kind = "reload"
		default:
			m := playerPosition.FindStringSubmatch(line)
			if m == nil {
				return Event{}, false, nil
			}
			position, err := strconv.ParseFloat(m[2], 64)
			if err != nil || math.IsNaN(position) || math.IsInf(position, 0) {
				return Event{}, false, nil
			}
			e.Kind, e.Position = "sync", position
			if m[1] != "Syncing video to" {
				e.Kind = "seek"
			} else if threshold := syncThreshold.FindStringSubmatch(line); threshold != nil {
				if threshold[1] == "0.85" {
					e.Kind = "local-sync"
				} else {
					e.Kind = "room-sync"
				}
			}
		}
		e.SongID, e.external = playbackMedia(line)
	default:
		return Event{}, false, nil
	}
	return Event{Playback: e}, true, nil
}

var externalMediaSeed = maphash.MakeSeed()

func playbackMedia(line string) (int64, uint64) {
	var external uint64
	for _, word := range strings.Fields(line) {
		if !strings.HasPrefix(word, "http://") && !strings.HasPrefix(word, "https://") {
			continue
		}
		word = strings.TrimRight(word, ",;")
		u, err := url.Parse(word)
		if err != nil || u.Hostname() == "" {
			continue
		}
		if u.Hostname() != "api.udon.dance" || u.Path != "/Api/Songs/play" {
			// LoadRoutedURL can include both the source and its redirect.
			if external == 0 {
				external = maphash.String(externalMediaSeed, word) | 1
			}
			continue
		}
		id, err := strconv.ParseInt(u.Query().Get("id"), 10, 64)
		if err == nil && id > 0 {
			return id, 0
		}
	}
	return 0, external
}
