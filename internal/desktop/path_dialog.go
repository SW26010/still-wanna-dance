package desktop

// PathDialog selects a filesystem path without opening or creating the file.
type PathDialog struct {
	Mode    string `json:"mode"` // open, database (existing or new), or folder
	Title   string `json:"title"`
	Initial string `json:"initial"`
}
