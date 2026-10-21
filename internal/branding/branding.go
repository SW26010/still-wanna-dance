// Package branding shares the application artwork between the WebUI and desktop.
// The Windows resource is checked in so ordinary builds also have an EXE icon.
//
//go:generate go run github.com/akavel/rsrc@v0.10.2 -arch amd64 -ico logo.ico -o logo_windows_amd64.syso
package branding

import (
	_ "embed"
	"encoding/binary"
	"fmt"
)

// SVG shares one geometry between the approved light palette and original dark palette.
//
//go:embed logo.svg
var SVG []byte

// ICO is generated from logo.svg by scripts/generate-logo.mjs.
//
//go:embed logo.ico
var ICO []byte

// LightICO is generated from the light palette in logo.svg.
//
//go:embed logo-light.ico
var LightICO []byte

// IconResource returns an encoded ICO image for CreateIconFromResourceEx.
// Prefer the smallest frame at least as large as the requested display size.
// The caller must copy it into DWORD-aligned storage before calling Win32.
func IconResource(size int, light bool) ([]byte, error) {
	ico := ICO
	if light {
		ico = LightICO
	}
	if size <= 0 || len(ico) < 6 || binary.LittleEndian.Uint16(ico[0:2]) != 0 || binary.LittleEndian.Uint16(ico[2:4]) != 1 {
		return nil, fmt.Errorf("invalid icon size or directory")
	}
	count := int(binary.LittleEndian.Uint16(ico[4:6]))
	if count == 0 || len(ico) < 6+count*16 {
		return nil, fmt.Errorf("invalid icon directory length")
	}
	best, bestSize := -1, 0
	for i := 0; i < count; i++ {
		entry := 6 + i*16
		width := int(ico[entry])
		if width == 0 {
			width = 256
		}
		if best < 0 || (bestSize < size && width > bestSize) || (width >= size && width < bestSize) {
			best, bestSize = entry, width
		}
	}
	length := uint64(binary.LittleEndian.Uint32(ico[best+8 : best+12]))
	offset := uint64(binary.LittleEndian.Uint32(ico[best+12 : best+16]))
	if length == 0 || offset < uint64(6+count*16) || offset+length > uint64(len(ico)) {
		return nil, fmt.Errorf("invalid icon image bounds")
	}
	return ico[offset : offset+length], nil
}
