package applog

// IsLogFile recognizes exactly the current and retained historical filenames.
func IsLogFile(name string) bool {
	_, _, current := segmentID(name)
	_, _, old := parseSegmentID(name, oldSegmentLayout)
	return current || old || legacyOrder(name) != 0
}
