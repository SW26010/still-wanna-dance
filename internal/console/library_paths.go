package console

import "still-wanna-dance/internal/cacheproxy"

func libraryPaths(s Settings) cacheproxy.LibraryPaths {
	return cacheproxy.LibraryPaths{DatabasePath: s.DatabasePath, VideoDir: s.VideoDir}
}
