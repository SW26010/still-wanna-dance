package cacheproxy

import "path/filepath"

// Existing fixtures deliberately retain their original layout. Readers receive
// explicit resources just like independently located databases and videos.
func fixtureLibrary(root string) LibraryPaths {
	return LibraryPaths{DatabasePath: storagePath(root), VideoDir: filepath.Join(root, "videos")}
}
