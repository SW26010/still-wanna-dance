package desktop

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
)

// Resolve junctions and short names as well as symbolic links. Go's lexical
// path cleanup alone does not give the final directory behind a junction.
func canonicalExistingPath(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(p, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
	if err != nil {
		return "", err
	}
	if n >= uint32(len(buf)) {
		return "", fmt.Errorf("实际路径过长：%s", path)
	}
	resolved := windows.UTF16ToString(buf[:n])
	if strings.HasPrefix(resolved, `\\?\UNC\`) {
		return `\\` + strings.TrimPrefix(resolved, `\\?\UNC\`), nil
	}
	return strings.TrimPrefix(resolved, `\\?\`), nil
}

// Creation time prevents a recycled PID from validating an abandoned record.
func processIdentity(pid uint32) (string, string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 32768)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return "", "", err
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return "", "", err
	}
	stamp := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	return windows.UTF16ToString(buf[:size]), strconv.FormatUint(stamp, 10), nil
}
