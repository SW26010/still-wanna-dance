package console

import (
	"net/http/httptest"
	"testing"
)

func TestRestartAuthenticationAndFirstShutdownWins(t *testing.T) {
	for _, first := range []string{"exit", "restart"} {
		t.Run(first, func(t *testing.T) {
			c := testConsole(t)
			for _, tc := range []struct{ method, host, token, origin string }{
				{"GET", c.address, c.token, ""},
				{"POST", "evil.invalid", c.token, ""},
				{"POST", c.address, "", ""},
				{"POST", c.address, c.token, "http://evil.invalid"},
			} {
				r := httptest.NewRequest(tc.method, "http://"+tc.host+"/api/restart", nil)
				r.Header.Set("X-StepStash-Token", tc.token)
				r.Header.Set("Origin", tc.origin)
				w := httptest.NewRecorder()
				c.ServeHTTP(w, r)
				if w.Code < 400 {
					t.Fatal("invalid restart accepted")
				}
				select {
				case <-c.RestartRequested():
					t.Fatal("invalid request signaled restart")
				default:
				}
			}
			for _, action := range []string{first, "restart", "exit"} {
				r := httptest.NewRequest("POST", "http://"+c.address+"/api/"+action, nil)
				r.Header.Set("X-StepStash-Token", c.token)
				w := httptest.NewRecorder()
				c.ServeHTTP(w, r)
				if w.Code != 200 || !w.Flushed {
					t.Fatal("shutdown was not acknowledged before signaling")
				}
			}
			select {
			case <-c.RestartRequested():
				if first != "restart" {
					t.Fatal("exit changed into restart")
				}
			default:
				if first == "restart" {
					t.Fatal("restart missing")
				}
			}
			select {
			case <-c.ExitRequested():
				if first != "exit" {
					t.Fatal("restart changed into exit")
				}
			default:
				if first == "exit" {
					t.Fatal("exit missing")
				}
			}
		})
	}
}
