package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeLease struct{}

func (fakeLease) Close() error { return nil }

func TestElectionWaitsForOwnerAndRecovers(t *testing.T) {
	for _, becomesOwner := range []bool{false, true} {
		tries, opens := 0, 0
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		lease, err := elect(ctx, func() (Lease, error) {
			tries++
			if becomesOwner && tries == 2 {
				return fakeLease{}, nil
			}
			return nil, nil
		}, func(context.Context) (bool, error) { return !becomesOwner && tries == 2, nil }, func() error { opens++; return nil })
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if becomesOwner && (lease == nil || opens != 0) {
			t.Fatal("failed to replace dead starter")
		}
		if !becomesOwner && (lease != nil || opens != 1) {
			t.Fatal("duplicate did not activate owner exactly once")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := elect(ctx, func() (Lease, error) { return nil, nil }, func(context.Context) (bool, error) { return false, nil }, nil); err == nil {
		t.Fatal("unready owner accepted")
	}
	if _, err := elect(context.Background(), func() (Lease, error) { return nil, nil }, func(context.Context) (bool, error) { return true, nil }, func() error { return errors.New("browser unavailable") }); err == nil {
		t.Fatal("browser failure hidden")
	}
}

func TestProbeOnlyActivatesOurIdentity(t *testing.T) {
	expected, err := NewIdentity(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	valid, _ := json.Marshal(expected)
	for _, body := range []string{string(valid), `{"app":"swd-console","protocol":1}`, `{"app":"other","protocol":1}`, `<title>Still Wanna Dance</title>`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/identity" {
				t.Error(r.URL.Path)
			}
			w.Write([]byte(body))
		}))
		got := probe(context.Background(), strings.TrimPrefix(s.URL, "http://"), expected)
		s.Close()
		if got != (body == string(valid)) {
			t.Fatal(body, got)
		}
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://example.invalid/", 302) }))
	defer s.Close()
	if probe(context.Background(), strings.TrimPrefix(s.URL, "http://"), expected) {
		t.Fatal("followed identity redirect")
	}
}
func TestConfigLifetimeLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	first, err := LockConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := LockConfig(p); err == nil {
		other.Close()
		t.Fatal("second writer acquired config")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal("close not idempotent", err)
	}
	second, err := LockConfig(p)
	if err != nil {
		t.Fatal("stale file prevented restart", err)
	}
	second.Close()
}

func TestMenuMatchesRuntime(t *testing.T) {
	items := Menu(State{CDN: true}, false)
	if len(items) != 5 || items[2].Label != "关闭本地播放网关" || items[2].Disabled || items[4].ID != Exit {
		t.Fatal(items)
	}
	items = Menu(State{}, true)
	if items[2].Label != "启动本地播放网关" || !items[2].Disabled || items[0].Disabled || items[4].Disabled {
		t.Fatal(items)
	}
}
