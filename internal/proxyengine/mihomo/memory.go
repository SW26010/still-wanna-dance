package mihomo

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/metacubex/mihomo/component/memory"
)

// The pinned native /memory handler writes DefaultManager.memory while the
// native /connections stream reads it without synchronization. Read the same
// process metric directly, keeping that shared native cache immutable. This is
// a transport compatibility shim, not a separate connection/statistics manager.
func serveMemory(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	var ws *websocket.Conn
	if strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
		var err error
		ws, err = websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		ctx = ws.CloseRead(ctx)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stat, err := memory.GetMemoryInfo(int32(os.Getpid()))
			if err != nil {
				return
			}
			data, _ := json.Marshal(struct {
				Inuse   uint64 `json:"inuse"`
				OSLimit uint64 `json:"oslimit"`
			}{Inuse: stat.RSS})
			if ws != nil {
				err = ws.Write(ctx, websocket.MessageText, data)
			} else {
				_, err = w.Write(append(data, '\n'))
				if err == nil {
					err = http.NewResponseController(w).Flush()
				}
			}
			if err != nil {
				return
			}
		}
	}
}
