package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"

	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/workfiles"
)

// The port is a hint, not an authorization or a promise that it is available.
// Callers hold the configuration lease while reading and writing it.
func readConsolePort(config string) int {
	path := apppaths.ForConfig(config).State("webui-port")
	info, err := os.Lstat(path)
	if err != nil || !workfiles.Regular(info) || info.Size() > 1024 {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1025))
	var record struct {
		Port int `json:"port"`
	}
	if err != nil || len(data) > 1024 || json.Unmarshal(data, &record) != nil || record.Port < 1 || record.Port > 65535 {
		return 0
	}
	return record.Port
}

func listenConsole(address, config string, automatic bool, restartPort int) (net.Listener, error) {
	if automatic {
		port := restartPort
		if port == 0 {
			port = readConsolePort(config)
		}
		if port != 0 {
			preferred := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			listener, err := net.Listen("tcp4", preferred)
			if err == nil {
				return listener, nil
			}
			slog.Warn("首选 WebUI 端口不可用", "event_name", "console_preferred_port_unavailable", "address", preferred, "error", err)
		}
	}
	return net.Listen("tcp4", address)
}

func saveConsolePort(config string, address net.Addr) error {
	tcp, ok := address.(*net.TCPAddr)
	if !ok || tcp.Port < 1 || tcp.Port > 65535 {
		return fmt.Errorf("无效的 WebUI 端口：%v", address)
	}
	if readConsolePort(config) == tcp.Port {
		return nil
	}
	data, err := json.Marshal(struct {
		Port int `json:"port"`
	}{Port: tcp.Port})
	if err != nil {
		return err
	}
	return workfiles.Write(apppaths.ForConfig(config).State("webui-port"), data)
}
