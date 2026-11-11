package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// PathDialog selects a filesystem path without opening or creating the file.
type PathDialog struct {
	Mode    string `json:"mode"` // open, database (existing or new), or folder
	Title   string `json:"title"`
	Initial string `json:"initial"`
}

type pathDialogResult struct {
	Path string `json:"path"`
}

// RunPathDialog serves one internal helper request. It never initializes the
// application, reads configuration, or creates the selected file/directory.
func RunPathDialog(input io.Reader, output io.Writer) error {
	var dialog PathDialog
	decoder := json.NewDecoder(io.LimitReader(input, 32*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&dialog); err != nil {
		return fmt.Errorf("无效的路径选择请求：%w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("无效的路径选择请求")
	}
	if dialog.Mode != "open" && dialog.Mode != "database" && dialog.Mode != "folder" {
		return errors.New("无效的路径选择模式")
	}
	if len(dialog.Title) > 4096 || len(dialog.Initial) > 4096 || strings.ContainsRune(dialog.Title+dialog.Initial, 0) {
		return errors.New("无效的路径选择请求")
	}
	path, err := showPathDialog(dialog)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(pathDialogResult{Path: path})
}
