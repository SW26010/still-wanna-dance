// replay applies the experimental policy to recorded log/request arrivals.
// It makes no network requests and emits selected decisions, never raw log lines.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	model "still-wanna-dance/experiments/playback-model"
	"still-wanna-dance/internal/vrclog"
)

type request struct {
	ID                int
	At                time.Time
	EndAt             *time.Time
	Start, End, Bytes int64
	Player, Video     bool
}
type sample struct {
	ID             string
	Media          model.Media
	Start          time.Time
	ReleaseSeconds float64
	Missing        int64
	Requests       []request
}
type input struct {
	Log              string
	UTCOffsetSeconds int
	Cases            []sample
}
type logged struct {
	at    time.Time
	event vrclog.Event
}
type result struct {
	Sample   string     `json:"sample"`
	Margin   float64    `json:"marginSeconds"`
	Delay    float64    `json:"pollDelaySeconds"`
	Requests []decision `json:"requests"`
}
type decision struct {
	ID      int      `json:"id"`
	T       float64  `json:"t"`
	Reject  bool     `json:"reject"`
	Reason  string   `json:"reason"`
	NeedByT *float64 `json:"needByT,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	in := flag.String("input", "", "private input JSON containing log path and selected recorded HTTP requests")
	enabled := flag.Bool("enabled", false, "enable experimental decisions; default pass-through")
	flag.Parse()
	if *in == "" {
		return fmt.Errorf("-input required")
	}
	b, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	var data input
	if err = json.Unmarshal(b, &data); err != nil {
		return err
	}
	time.Local = time.FixedZone("recorded-log", data.UTCOffsetSeconds)
	f, err := os.Open(data.Log)
	if err != nil {
		return err
	}
	defer f.Close()
	var logs []logged
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64<<10), 1<<20)
	for s.Scan() {
		line := s.Text()
		e, ok, err := vrclog.ParseWithPlayback(line)
		if err != nil {
			continue
		}
		if !ok || len(line) < 19 {
			continue
		}
		at, err := time.ParseInLocation("2006.01.02 15:04:05", line[:19], time.Local)
		if err != nil {
			continue
		}
		logs = append(logs, logged{at, e})
	}
	if err = s.Err(); err != nil {
		return err
	}
	// Preserve recorded order within each second. Real logs have second precision.
	sort.SliceStable(logs, func(i, j int) bool { return logs[i].at.Before(logs[j].at) })
	var results []result
	for _, margin := range []time.Duration{0, 2 * time.Second, 5 * time.Second, 10 * time.Second} {
		for _, delay := range []time.Duration{0, 2 * time.Second, 3 * time.Second} {
			for _, c := range data.Cases {
				m := model.New(model.Options{Enabled: *enabled, Margin: margin, MaxEventDelay: 4 * time.Second})
				cursor := 0
				r := result{Sample: c.ID, Margin: margin.Seconds(), Delay: delay.Seconds()}
				for i, q := range c.Requests {
					if !q.Player {
						continue
					}
					// Timestamp precision is one second: never use a same-second
					// error that may have been caused by this recorded response.
					for cursor < len(logs) && !logs[cursor].at.Add(time.Second+delay).After(q.At) {
						m.Observe(logs[cursor].event, logs[cursor].at.Add(time.Second+delay))
						cursor++
					}
					var previous *model.Transfer
					for j := i - 1; j >= 0; j-- {
						p := c.Requests[j]
						if p.Player && p.Video && p.EndAt != nil && !p.EndAt.After(q.At) {
							previous = &model.Transfer{SongID: c.Media.SongID, Key: c.Media.Key, Start: p.Start, End: p.End, DeliveredThrough: p.Start + p.Bytes, Missing: c.Missing}
							break
						}
					}
					d := m.Admit(q.At, c.Media, previous, model.Request{SongID: c.Media.SongID, Key: c.Media.Key, Start: q.Start, End: q.End, Missing: c.Missing, BlockReady: q.At.Sub(c.Start).Seconds() >= c.ReleaseSeconds})
					out := decision{ID: q.ID, T: q.At.Sub(c.Start).Seconds(), Reject: d.Reject, Reason: d.Reason}
					if !d.NeedBy.IsZero() {
						t := d.NeedBy.Sub(c.Start).Seconds()
						out.NeedByT = &t
					}
					r.Requests = append(r.Requests, out)
				}
				results = append(results, r)
			}
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Enabled bool     `json:"enabled"`
		Method  string   `json:"method"`
		Results []result `json:"results"`
	}{*enabled, strings.Join([]string{"Recorded traces, not counterfactual playback outcomes.", "Role and sync target parsed from original log; events become available at the end of their logged second plus poll delay, avoiding same-second lookahead.", "Completed previous video responses only; overlapping progress needs live snapshots and is not inferred from final bytes.", "Missing block and release are known controlled-fixture inputs; production readiness is not implemented here."}, " "), results})
}
