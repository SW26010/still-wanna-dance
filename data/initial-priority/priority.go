// Package initialpriority contains the anonymous, bundled cold-start prior.
package initialpriority

import (
	_ "embed"
	"encoding/json"
)

//go:embed song-priority.json
var encoded []byte

var scores = func() map[int64]float64 {
	var data struct {
		Songs []struct {
			SongID int64   `json:"songId"`
			Score  float64 `json:"score"`
		} `json:"songs"`
	}
	if err := json.Unmarshal(encoded, &data); err != nil {
		panic(err)
	}
	result := make(map[int64]float64, len(data.Songs))
	for _, song := range data.Songs {
		result[song.SongID] = song.Score
	}
	return result
}()

func Score(id int64) float64 { return scores[id] }
