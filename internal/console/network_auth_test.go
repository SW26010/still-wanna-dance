package console

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSOCKS5Authentication(t *testing.T) {
	c := testConsole(t)
	address := socksPeerWithAuth(t, "test-user", "test-secret", func(conn net.Conn, target string) {
		if target != "remote.invalid:443" {
			t.Error("target was not delegated")
		}
		socksReply(conn, 0)
		io.WriteString(conn, "ok")
	})
	for _, password := range []string{"test-secret", "wrong-secret"} {
		s := c.settings
		s.UpstreamMode, s.SOCKS5Address = "socks5", address
		s.SOCKS5Username, s.SOCKS5Password = "test-user", password
		dial, client, err := c.networkFor(s)
		if client != nil {
			defer client.CloseIdleConnections()
		}
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, err := dial(ctx, "tcp", "remote.invalid:443")
		cancel()
		if password == "wrong-secret" {
			if err == nil {
				conn.Close()
				t.Fatal("accepted incorrect credentials")
			}
			if strings.Contains(err.Error(), password) {
				t.Fatal("error exposed password")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(conn)
		conn.Close()
		if err != nil || string(body) != "ok" {
			t.Fatal("authenticated connection failed", err)
		}
	}
	for _, pair := range [][2]string{{"user", ""}, {"", "secret"}, {strings.Repeat("a", 256), "secret"}, {"user", strings.Repeat("密", 86)}} {
		if _, err := NewAuthenticatedSOCKS5Dialer(address, pair[0], pair[1]); err == nil {
			t.Fatal("accepted invalid credentials")
		}
	}
}

func TestSOCKS5PasswordSettingsLifecycle(t *testing.T) {
	c := testConsole(t)
	s := c.settings
	s.UpstreamMode, s.SOCKS5Address = "socks5", "127.0.0.1:1080"
	s.SOCKS5Username, s.SOCKS5Password = "test-user", "private-test-secret"
	if err := c.save(s); err != nil {
		t.Fatal(err)
	}
	loaded, err := New(c.configPath, c.address)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if loaded.settings.SOCKS5Password != s.SOCKS5Password {
		t.Fatal("secret not persisted")
	}
	if err := c.writeSnapshot("inventory", s, Inventory{}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := os.ReadFile(c.configPath + ".inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(snapshot), s.SOCKS5Password) || strings.Contains(string(snapshot), s.SOCKS5Username) {
		t.Fatal("snapshot contains credentials")
	}
	if _, err := readSnapshot[Inventory](c, "inventory"); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://"+c.address+path, strings.NewReader(body))
		r.Header.Set("X-StepStash-Token", c.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		return w
	}
	w := request("GET", "/api/status", "")
	if strings.Contains(w.Body.String(), s.SOCKS5Password) || strings.Contains(w.Body.String(), `"socks5Password":`) {
		t.Fatal("status exposed secret")
	}
	if !strings.Contains(w.Body.String(), `"socks5PasswordSet":true`) {
		t.Fatal("missing password state")
	}
	update := func(password *string) {
		t.Helper()
		payload := c.settings
		payload.SOCKS5Password = ""
		body, _ := json.Marshal(payload)
		var fields map[string]any
		json.Unmarshal(body, &fields)
		if password != nil {
			fields["socks5Password"] = *password
		}
		body, _ = json.Marshal(fields)
		w := request("POST", "/api/settings", string(body))
		if w.Code != http.StatusOK {
			t.Fatalf("settings: %d %s", w.Code, w.Body.String())
		}
	}
	oldClient := c.client
	update(nil)
	if c.settings.SOCKS5Password != s.SOCKS5Password || c.client != oldClient {
		t.Fatal("omitted password was not preserved")
	}
	replacement := "replacement-test-secret"
	update(&replacement)
	if c.settings.SOCKS5Password != replacement || c.client == oldClient {
		t.Fatal("credential change did not refresh transport")
	}
	c.settings.SOCKS5Username = ""
	empty := ""
	update(&empty)
	data, err := os.ReadFile(c.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.settings.SOCKS5Password != "" || strings.Contains(string(data), replacement) {
		t.Fatal("clear did not remove stored secret")
	}
}
