// Package branding shares the application artwork between the WebUI and desktop.
package branding

import (
	_ "embed"
	"encoding/binary"
	"fmt"
)

// SVG is the original user-supplied artwork, without recoloring or cropping.
//
//go:embed logo.svg
var SVG []byte

// ICO is generated from logo.svg by scripts/generate-logo.mjs.
//
//go:embed logo.ico
var ICO []byte

// IconResource returns an encoded ICO image for CreateIconFromResourceEx.
// Prefer the smallest frame at least as large as the requested display size.
// The caller must copy it into DWORD-aligned storage before calling Win32.
func IconResource(size int) ([]byte, error) {
	if size <= 0 || len(ICO) < 6 || binary.LittleEndian.Uint16(ICO[0:2]) != 0 || binary.LittleEndian.Uint16(ICO[2:4]) != 1 {
		return nil, fmt.Errorf("invalid icon size or directory")
	}
	count := int(binary.LittleEndian.Uint16(ICO[4:6]))
	if count == 0 || len(ICO) < 6+count*16 {
		return nil, fmt.Errorf("invalid icon directory length")
	}
	best, bestSize := -1, 0
	for i := 0; i < count; i++ {
		entry := 6 + i*16
		width := int(ICO[entry])
		if width == 0 {
			width = 256
		}
		if best < 0 || (bestSize < size && width > bestSize) || (width >= size && width < bestSize) {
			best, bestSize = entry, width
		}
	}
	length := uint64(binary.LittleEndian.Uint32(ICO[best+8 : best+12]))
	offset := uint64(binary.LittleEndian.Uint32(ICO[best+12 : best+16]))
	if length == 0 || offset < uint64(6+count*16) || offset+length > uint64(len(ICO)) {
		return nil, fmt.Errorf("invalid icon image bounds")
	}
	return ICO[offset : offset+length], nil
}
