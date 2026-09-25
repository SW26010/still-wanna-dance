package desktop

import (
	"encoding/binary"
	"net"
	"path/filepath"
	"syscall"
	"unsafe"
)

func portOwner(host string, port uint16) *Owner {
	proc := syscall.NewLazyDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")
	var size uint32
	r, _, _ := proc.Call(0, uintptr(unsafe.Pointer(&size)), 0, 2, 3, 0)
	if r != 122 || size < 4 || size > 16<<20 {
		return nil
	}
	for i := 0; i < 3; i++ {
		b := make([]byte, size)
		r, _, _ = proc.Call(uintptr(unsafe.Pointer(&b[0])), uintptr(unsafe.Pointer(&size)), 0, 2, 3, 0)
		if r == 122 {
			if size > 16<<20 {
				return nil
			}
			continue
		}
		if r != 0 {
			return nil
		}
		count := int(binary.LittleEndian.Uint32(b))
		for n := 0; n < count; n++ {
			off := 4 + n*24
			if off+24 > len(b) {
				return nil
			}
			row := b[off : off+24]
			addr := net.IP(row[4:8])
			if binary.BigEndian.Uint16(row[8:10]) != port || (!addr.IsUnspecified() && addr.String() != host) {
				continue
			}
			pid := binary.LittleEndian.Uint32(row[20:24])
			name := "未知进程"
			h, err := syscall.OpenProcess(0x1000, false, pid)
			if err == nil {
				buf := make([]uint16, 32768)
				length := uint32(len(buf))
				ok, _, _ := kernel32.NewProc("QueryFullProcessImageNameW").Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&length)))
				syscall.CloseHandle(h)
				if ok != 0 {
					name = filepath.Base(syscall.UTF16ToString(buf[:length]))
				}
			}
			return &Owner{PID: pid, Name: name}
		}
		return nil
	}
	return nil
}
