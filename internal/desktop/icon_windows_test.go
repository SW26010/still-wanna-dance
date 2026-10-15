package desktop

import (
	"fmt"
	"syscall"
	"testing"
	"unsafe"
)

// Exercise Windows' actual icon decoder without registering a tray icon or
// starting the application. Includes PNG frames and a size between ICO frames.
func TestBrandIconWindowsDecode(t *testing.T) {
	gdi32 := syscall.NewLazyDLL("gdi32.dll")
	for _, size := range []int{16, 20, 24, 32, 36, 40, 48, 64, 128, 256} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			icon, err := makeIconAtSize(size)
			if err != nil {
				t.Fatal(err)
			}
			defer user32.NewProc("DestroyIcon").Call(icon)
			var info struct {
				IsIcon, XHotspot, YHotspot uint32
				Mask, Color                uintptr
			}
			if ok, _, err := user32.NewProc("GetIconInfo").Call(icon, uintptr(unsafe.Pointer(&info))); ok == 0 {
				t.Fatal(err)
			}
			defer gdi32.NewProc("DeleteObject").Call(info.Mask)
			defer gdi32.NewProc("DeleteObject").Call(info.Color)
			var bitmap struct {
				Type, Width, Height, WidthBytes int32
				Planes, BitsPixel               uint16
				Bits                            uintptr
			}
			if n, _, err := gdi32.NewProc("GetObjectW").Call(info.Color, unsafe.Sizeof(bitmap), uintptr(unsafe.Pointer(&bitmap))); n == 0 {
				t.Fatal(err)
			}
			if bitmap.Width != int32(size) || bitmap.Height != int32(size) || bitmap.BitsPixel != 32 {
				t.Fatalf("wrong icon bitmap: %+v", bitmap)
			}
		})
	}
}
