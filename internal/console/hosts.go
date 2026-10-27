package console

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const marker = "# SWD managed"

// Playback is served at the API entry, so no resource domains need to be
// presumed or mapped before the API returns them.
var domains = []string{"api.udon.dance"}

func hostsPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("SystemRoot"), "System32", "drivers", "etc", "hosts")
	}
	return "/etc/hosts"
}

type HostsStatus struct {
	Managed             bool   `json:"managed"`
	ExitRestorePrepared bool   `json:"exitRestorePrepared"`
	AuthorizationID     string `json:"authorizationID,omitempty"`
	Ready               bool   `json:"ready"`
	Conflict            bool   `json:"conflict"`
	Message             string `json:"message"`
}

func hasManagedHosts(data string) bool {
	for _, line := range strings.Split(data, "\n") {
		if !strings.Contains(line, marker) {
			continue
		}
		body, _, _ := strings.Cut(line, "#")
		fields := strings.Fields(body)
		if len(fields) < 2 {
			continue
		}
		// Only current API mappings are ours. Keep recognizing their edited
		// address or formatting so restoration can report unsafe changes.
		for _, host := range fields[1:] {
			for _, domain := range domains {
				if strings.EqualFold(strings.TrimSuffix(host, "."), domain) {
					return true
				}
			}
		}
	}
	return false
}

func inspectHosts(data string) (result HostsStatus) {
	defer func() { result.Managed = hasManagedHosts(data) }()
	found := map[string]bool{}
	conflict := false
	for _, line := range strings.Split(data, "\n") {
		body, _, _ := strings.Cut(line, "#")
		fields := strings.Fields(body)
		if len(fields) < 2 {
			continue
		}
		for _, host := range fields[1:] {
			host = strings.ToLower(strings.TrimSuffix(host, "."))
			for _, d := range domains {
				if host == d {
					if fields[0] == "127.0.0.1" {
						found[d] = true
					} else {
						conflict = true
					}
				}
			}
		}
	}
	if conflict {
		return HostsStatus{Conflict: true, Message: "发现已有冲突映射，请先检查 hosts；不会覆盖其他程序的配置。"}
	}
	if len(found) == len(domains) {
		return HostsStatus{Ready: true, Message: "播放 API 已接入本地；资源域名由 API 返回。"}
	}
	return HostsStatus{Message: "尚未接入游戏，请在加速页点击「开始加速」。"}
}

func (h *hostsController) status() HostsStatus {
	var s HostsStatus
	b, err := os.ReadFile(h.path)
	if err != nil {
		s.Message = err.Error()
	} else {
		s = inspectHosts(string(b))
	}
	s.ExitRestorePrepared = h.prepared.Load()
	if id := h.authorizationID.Load(); id != nil {
		s.AuthorizationID = *id
	}
	return s
}

func transformHosts(data, action string) (string, error) {
	if action == "enable" {
		s := inspectHosts(data)
		if s.Conflict {
			return "", fmt.Errorf("%s", s.Message)
		}
		if s.Ready {
			return data, nil
		}
		// Append only mappings not already supplied by the user.
		for _, d := range domains {
			found := false
			for _, line := range strings.Split(data, "\n") {
				f := strings.Fields(strings.SplitN(line, "#", 2)[0])
				if len(f) < 2 {
					continue
				}
				for _, h := range f[1:] {
					if strings.EqualFold(strings.TrimSuffix(h, "."), d) && f[0] == "127.0.0.1" {
						found = true
					}
				}
			}
			if !found {
				data += "\r\n127.0.0.1 " + d + " " + marker + "\r\n"
			}
		}
		return data, nil
	}
	if action != "disable" {
		return "", fmt.Errorf("unknown hosts action")
	}
	for _, d := range domains {
		data = strings.ReplaceAll(data, "\r\n127.0.0.1 "+d+" "+marker+"\r\n", "")
	}
	if hasManagedHosts(data) {
		return "", fmt.Errorf("Still Wanna Dance 的标记条目已被修改，请手动检查 hosts")
	}
	return data, nil
}

func applyHosts(p, action string) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	next, err := transformHosts(string(b), action)
	if err != nil {
		return err
	}
	if next == string(b) {
		return nil
	}
	return writeHostsChange(p, b, []byte(next))
}

func writeHostsChange(p string, before, after []byte) error {
	// Keep the first pre-change snapshot; restore removes only our exact entries.
	backup, err := os.OpenFile(p+".swd-backup", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		_, err = backup.Write(before)
		closeErr := backup.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	} else if !os.IsExist(err) {
		return err
	}
	return os.WriteFile(p, after, 0644)
}

func portAvailable(address string) error {
	l, err := net.Listen("tcp4", address)
	if err != nil {
		return err
	}
	return l.Close()
}
