package console

import "net/http"

func (c *Console) serveHelp(w http.ResponseWriter) {
	c.serveWebUI(w, "help", "使用帮助", nil)
}
