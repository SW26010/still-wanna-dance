//go:build !windows && !linux

package cacheproxy

import "os"

func publishImportedVideo(stage, destination string) error { return os.Link(stage, destination) }
