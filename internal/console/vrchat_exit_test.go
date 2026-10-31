package console

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
)

func TestExitWithVRChatSetting(t *testing.T) {
	for _, value := range []string{"", `,"exitWithVRChat":null`, `,"exitWithVRChat":false`, `,"exitWithVRChat":true`} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(`{"schemaVersion":8`+value+`}`), 0600); err != nil {
			t.Fatal(err)
		}
		loaded, err := loadConfigFile(path)
		if err != nil || loaded.Settings.ExitWithVRChat != (value == `,"exitWithVRChat":true`) {
			t.Fatal(value, loaded.Settings.ExitWithVRChat, err)
		}
	}
	c := testConsole(t)
	if err := c.saveSettingsPatch([]byte(`{"exitWithVRChat":true}`)); err != nil {
		t.Fatal(err)
	}
	if !c.savedSettings.ExitWithVRChat || c.settings.ExitWithVRChat {
		t.Fatal("save did not stage the exit setting")
	}
	c.watchVRChat(func(context.Context) error { t.Fatal("staged setting started monitoring"); return nil })
	for _, patch := range []string{`{"logLevel":"warn"}`, `{"exitWithVRChat":null}`} {
		if err := c.saveSettingsPatch([]byte(patch)); err != nil {
			t.Fatal(err)
		}
	}
	c = restartTestConsole(t, c)
	if !c.settings.ExitWithVRChat {
		t.Fatal("restart lost the saved exit setting")
	}
	if err := c.saveSettingsPatch([]byte(`{"exitWithVRChat":false}`)); err != nil {
		t.Fatal(err)
	}
	if c.savedSettings.ExitWithVRChat || !c.settings.ExitWithVRChat {
		t.Fatal("disable did not follow the restart boundary")
	}
	c = restartTestConsole(t, c)
	if c.settings.ExitWithVRChat {
		t.Fatal("explicit false was not persisted")
	}
}

func TestVRChatExitRequestsOnlyConfirmedShutdown(t *testing.T) {
	for _, err := range []error{nil, context.Canceled, errors.New("observation failed")} {
		c := &Console{settings: Settings{ExitWithVRChat: true}, lifecycle: newApplicationLifecycle()}
		c.watchVRChat(func(context.Context) error { return err })
		want := ShutdownAction("running")
		if err == nil {
			want = ShutdownExit
		}
		if c.ShutdownAction() != want {
			t.Fatal(err, c.ShutdownAction(), want)
		}
	}
}

func TestVRChatMonitorStopsWithApplication(t *testing.T) {
	for _, action := range []ShutdownAction{ShutdownExit, ShutdownRestart, ""} {
		t.Run(string(action), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := &Console{settings: Settings{ExitWithVRChat: true}, lifecycle: newApplicationLifecycle()}
				calls := 0
				done := make(chan struct{})
				go func() {
					defer close(done)
					c.watchVRChat(func(ctx context.Context) error { calls++; <-ctx.Done(); return ctx.Err() })
				}()
				synctest.Wait()
				if action == "" {
					c.lifecycle.beginShutdown()
				} else {
					c.lifecycle.request(action, nil)
				}
				<-done
				want := action
				if want == "" {
					want = "running" // cleanup alone does not claim a planned exit
				}
				if calls != 1 || c.ShutdownAction() != want {
					t.Fatal(calls, c.ShutdownAction())
				}
			})
		})
	}
}
