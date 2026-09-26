package console

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const marker = "# StepStash managed"

var domains = []string{"play.udon.dance", "nya.xin.moe", "api.udon.dance"}

func hostsPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("SystemRoot"), "System32", "drivers", "etc", "hosts")
	}
	return "/etc/hosts"
}

type HostsStatus struct {
	Ready    bool   `json:"ready"`
	Conflict bool   `json:"conflict"`
	Message  string `json:"message"`
}

func inspectHosts(data string) HostsStatus {
	found := map[string]bool{}
	conflict := false
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(strings.SplitN(line, "#", 2)[0])
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
		return HostsStatus{Ready: true, Message: "播放 API 和两个视频域名均已接入本地。"}
	}
	return HostsStatus{Message: "尚未接入游戏，请点击「修改 hosts」。"}
}

func readHostsStatus() HostsStatus {
	b, err := os.ReadFile(hostsPath())
	if err != nil {
		return HostsStatus{Message: err.Error()}
	}
	return inspectHosts(string(b))
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
	if strings.Contains(data, marker) {
		return "", fmt.Errorf("StepStash 的标记条目已被修改，请手动检查 hosts")
	}
	return data, nil
}

// ApplyHosts is called only by the explicitly elevated helper invocation.
func ApplyHosts(action string) error {
	p := hostsPath()
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
	// Keep the first pre-change snapshot; restore removes only our exact entries.
	backup, err := os.OpenFile(p+".stepstash-backup", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		_, err = backup.Write(b)
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
	return os.WriteFile(p, []byte(next), 0644)
}

func portAvailable(address string) error {
	l, err := net.Listen("tcp4", address)
	if err != nil {
		return err
	}
	return l.Close()
}
