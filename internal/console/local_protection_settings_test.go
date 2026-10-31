package console

import (
	"context"
	"crypto/md5"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Use the production settings -> engine path, real files and HTTP consumption.
// All media is imported locally; no upstream or game is required.
func TestLocalProtectionSettingsControlEngineAfterRestart(t *testing.T) {
	c := testConsole(t)
	c.settings.VideoStorageMode = "unlimited"
	c.savedSettings = c.settings
	body := []byte("local protection settings integration")
	key := fmt.Sprintf("%x", md5.Sum(body))
	source := filepath.Join(t.TempDir(), key+".mp4")
	if err := os.WriteFile(source, body, 0600); err != nil {
		t.Fatal(err)
	}
	target := fmt.Sprintf("https://play.udon.dance/files/2403/1-abc.mp4?e=%s&s=%d", key, len(body))
	check := func(seconds int) {
		t.Helper()
		c.lifecycleMu.Lock()
		_, err := c.ensureEngineLocked()
		c.lifecycleMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		observeTestResources(t, c.resources)
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		if _, err := c.service.ImportVideo(ctx, source, map[string]bool{key: true}); err != nil {
			t.Fatal(err)
		}
		if hit, err := c.service.Prefetch(ctx, target); err != nil || hit != "HIT" {
			t.Fatal("local fixture not available", hit, err)
		}
		before := time.Now().UnixMilli()
		response := httptest.NewRecorder()
		c.service.ServeHTTP(response, httptest.NewRequest("GET", target, nil).WithContext(ctx))
		after := time.Now().UnixMilli()
		if response.Code != 200 || response.Body.String() != string(body) {
			t.Fatal("local GET failed", response.Code, response.Body.String())
		}
		sub, err := c.service.ObserveBlocks()
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
		for {
			snapshot, changed, closed := sub.Snapshot()
			if closed {
				t.Fatal("observation closed")
			}
			if snapshot != nil && snapshot.At >= after && snapshot.Used == 0 {
				block, exists := snapshot.Blocks[key+"/0"]
				if seconds == 0 {
					if exists {
						t.Fatal("zero grace retained an ended block", block)
					}
				} else if !exists || !block.Protected || block.TimeBasis != "ended" ||
					block.ProtectionUntil-block.At != int64(seconds)*1000 ||
					block.At < before || block.At > after {
					t.Fatal("engine did not use active protection setting", seconds, block)
				}
				return
			}
			select {
			case <-changed:
			case <-ctx.Done():
				t.Fatal("waiting for real GET protection", ctx.Err())
			}
		}
	}
	check(300)
	for _, seconds := range []int{123, 0} {
		active := c.settings.LocalProtectionGraceSeconds
		next := c.settings
		next.LocalProtectionGraceSeconds = seconds
		if err := c.save(next); err != nil {
			t.Fatal(err)
		}
		check(active)
		c = restartTestConsole(t, c)
		check(seconds)
	}
}
