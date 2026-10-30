// Package dashboard serves the unmodified, pinned zashboard distribution.
package dashboard

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed assets/zashboard.zip
var archive []byte

var Handler = staticHandler()

func staticHandler() http.Handler {
	z, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		panic(err)
	}
	files, err := fs.Sub(z, "dist")
	if err != nil {
		panic(err)
	}
	// ZIP entries do not implement Seek, which net/http needs for sniffing,
	// Content-Length and Range. Inflate this fixed release once into memory.
	contents := make(map[string][]byte)
	err = fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		contents[name], err = fs.ReadFile(files, name)
		return err
	})
	if err != nil {
		panic(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" || name == "." {
			name = "index.html"
		}
		data, ok := contents[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	})
}
