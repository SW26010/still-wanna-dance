package console

import (
	_ "embed"
	"html/template"
	"net/http"
)

//go:embed help.html
var helpPage string
var helpTemplate = template.Must(template.New("help").Parse(helpPage))

func (c *Console) serveHelp(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	p := c.files()
	_ = helpTemplate.Execute(w, struct{ Config, Root, Storage, Log string }{p.Config, p.Root, c.settings.StorageDir, p.Log})
}
