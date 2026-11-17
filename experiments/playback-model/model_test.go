package playbackmodel

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"still-wanna-dance/internal/vrclog"
)

var epoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

func setup(enabled, owner bool) (*Model, Media, *Transfer, Request) {
	m := New(Options{Enabled: enabled, Margin: 5 * time.Second, MaxEventDelay: 4 * time.Second})
	for _, e := range []vrclog.PlaybackEvent{{At: epoch, Kind: "role", Owner: &owner}, {At: epoch, Kind: "start", SongID: 1}, {At: epoch, Kind: "sync", Position: 0}} {
		m.Observe(vrclog.Event{Playback: &e}, epoch)
	}
	v := Media{SongID: 1, Key: "media", Bytes: 100 << 20, Seconds: 400, Explicit: true}
	p := &Transfer{SongID: 1, Key: v.Key, Start: 0, End: v.Bytes, DeliveredThrough: 2 << 20, Missing: 2 << 20}
	r := Request{SongID: 1, Key: v.Key, Start: (2 << 20) - 8192, End: v.Bytes, Missing: 2 << 20}
	return m, v, p, r
}

func TestAdmissionBoundariesAndLoop(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*Model, *Media, *Transfer, *Request)
	}{
		{"disabled", func(m *Model, _ *Media, _ *Transfer, _ *Request) { m.opts.Enabled = false }},
		{"owner", func(m *Model, _ *Media, _ *Transfer, _ *Request) { m.owner = true }},
		{"unknown-role", func(m *Model, _ *Media, _ *Transfer, _ *Request) { m.ownerKnown = false }},
		{"fallback", func(_ *Model, v *Media, _ *Transfer, _ *Request) { v.Explicit = false }},
		{"ready", func(_ *Model, _ *Media, _ *Transfer, r *Request) { r.BlockReady = true }},
		{"seek", func(_ *Model, _ *Media, _ *Transfer, r *Request) { r.Start = 10 << 20 }},
		{"end-changed", func(_ *Model, _ *Media, _ *Transfer, r *Request) { r.End-- }},
		{"version-changed", func(_ *Model, _ *Media, _ *Transfer, r *Request) { r.Key = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, v, p, r := setup(true, false)
			tc.alter(m, &v, p, &r)
			if m.Admit(epoch.Add(20*time.Second), v, p, r).Reject {
				t.Fatal("must pass through")
			}
		})
	}
	m, v, p, r := setup(true, false)
	if m.Admit(epoch.Add(13*time.Second), v, p, r).Reject {
		t.Fatal("exact margin boundary rejected")
	}
	if !m.Admit(epoch.Add(13*time.Second+time.Millisecond), v, p, r).Reject {
		t.Fatal("old unready suffix not rejected")
	}
	if m.Admit(epoch.Add(30*time.Second), v, p, r).Reject {
		t.Fatal("repeated504 loop")
	}
}

func TestOwnerABAtZeroMargin(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		enabled, allowOwner, known, reject bool
	}{
		{"A-disabled", false, true, true, false},
		{"B-owner-opt-in", true, true, true, true},
		{"owner-default-pass-through", true, false, true, false},
		{"B-role-independent", true, true, false, true},
		{"unknown-role-default-pass-through", true, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, v, p, r := setup(tc.enabled, true)
			m.opts.Margin = 0
			m.opts.AllowOwner = tc.allowOwner
			m.ownerKnown = tc.known
			if m.Admit(epoch.Add(8*time.Second), v, p, r).Reject {
				t.Fatal("must not reject at exact need-by")
			}
			if got := m.Admit(epoch.Add(8*time.Second+time.Millisecond), v, p, r).Reject; got != tc.reject {
				t.Fatalf("reject=%v, want %v", got, tc.reject)
			}
			if tc.reject {
				for _, e := range []vrclog.PlaybackEvent{
					{At: epoch.Add(9 * time.Second), Kind: "error", SongID: 1},
					{At: epoch.Add(15 * time.Second), Kind: "reload", SongID: 1},
					{At: epoch.Add(16 * time.Second), Kind: "start", SongID: 1},
					{At: epoch.Add(16 * time.Second), Kind: "sync", Position: 0},
				} {
					m.Observe(vrclog.Event{Playback: &e}, e.At)
				}
				if m.Admit(epoch.Add(30*time.Second), v, p, r).Reject {
					t.Fatal("owner returning to zero must not trigger another model rejection")
				}
			}
		})
	}
}

func TestTargetClockResetAndHandoff(t *testing.T) {
	m, v, _, _ := setup(true, false)
	// Sync target advances while the actual video can remain stalled at zero.
	e := vrclog.PlaybackEvent{At: epoch.Add(10 * time.Second), Kind: "sync", Position: 10}
	m.Observe(vrclog.Event{Playback: &e}, e.At.Add(3*time.Second))
	h := m.Handoffs(epoch.Add(20*time.Second), v, 2, 8, 1<<20)
	if len(h) != 2 || h[0].First != 2 || h[0].Last != 4 || h[0].Grace != 2*time.Second || h[1].First != 4 || h[1].Last != 8 || h[1].Grace != 10*time.Second {
		t.Fatalf("%+v", h)
	}
	m.Observe(vrclog.Event{Reset: true}, epoch.Add(21*time.Second))
	h = m.Handoffs(epoch.Add(21*time.Second), v, 2, 8, 1<<20)
	if len(h) != 1 || h[0].Grace != 2*time.Second {
		t.Fatal("room reset retained model")
	}
	m, v, p, r := setup(true, false)
	e = vrclog.PlaybackEvent{At: epoch.Add(time.Second), Kind: "sync", Position: 1}
	m.Observe(vrclog.Event{Playback: &e}, epoch.Add(20*time.Second))
	if m.Admit(epoch.Add(21*time.Second), v, p, r).Reject {
		t.Fatal("delayed historical log accepted")
	}
}

func TestRealHTTPRejectsBeforeDownstreamAndThenPasses(t *testing.T) {
	m, v, p, q := setup(true, false)
	calls := 0
	h := Admission(m, func() time.Time { return epoch.Add(20 * time.Second) }, func(*http.Request) (Media, *Transfer, Request, bool) { return v, p, q, true }, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(206); w.Write([]byte("video")) }))
	s := httptest.NewServer(h)
	defer s.Close()
	for i, want := range []int{504, 206} {
		r, err := http.Get(s.URL)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != want {
			t.Fatalf("%d %s", r.StatusCode, body)
		}
		if calls != i {
			t.Fatal("rejected request reached downstream")
		}
	}
}

func TestPlaybackTransitionsDoNotInventProgressOrRenewRejection(t *testing.T) {
	m, v, p, r := setup(true, false)
	if !m.Admit(epoch.Add(20*time.Second), v, p, r).Reject {
		t.Fatal("expected first rejection")
	}
	for _, e := range []vrclog.PlaybackEvent{
		{At: epoch.Add(21 * time.Second), Kind: "error", SongID: 1},
		{At: epoch.Add(26 * time.Second), Kind: "reload", SongID: 1},
		{At: epoch.Add(27 * time.Second), Kind: "start", SongID: 1},
		{At: epoch.Add(27 * time.Second), Kind: "sync", Position: 27},
		{At: epoch.Add(27 * time.Second), Kind: "start", SongID: 1},
	} {
		m.Observe(vrclog.Event{Playback: &e}, e.At)
	}
	if !m.synced {
		t.Fatal("duplicate start erased sync")
	}
	if m.Admit(epoch.Add(28*time.Second), v, p, r).Reject {
		t.Fatal("reload renewed rejection allowance")
	}
	playing := false
	e := vrclog.PlaybackEvent{At: epoch.Add(29 * time.Second), Kind: "transport", Playing: &playing}
	m.Observe(vrclog.Event{Playback: &e}, e.At)
	if h := m.Handoffs(epoch.Add(30*time.Second), v, 20, 22, 1<<20); len(h) != 1 || h[0].Grace != 2*time.Second {
		t.Fatal("pause kept model handoff")
	}
	m, v, p, r = setup(true, false)
	e = vrclog.PlaybackEvent{At: epoch.Add(50 * time.Second), Kind: "sync", Position: 0}
	m.Observe(vrclog.Event{Playback: &e}, e.At)
	if m.Admit(epoch.Add(51*time.Second), v, p, r).Reject {
		t.Fatal("explicit rewind kept old clock")
	}
	if m.Admit(epoch.Add(500*time.Second), v, p, r).Reject {
		t.Fatal("model outlived media duration")
	}
}
