package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// Instance owns the discovery record for as long as it owns the desktop mutex.
type Instance struct {
	lease Lease
	path  string
	once  sync.Once
	err   error
}

type instanceRecord struct {
	Address string `json:"address"`
	PID     uint32 `json:"pid"`
}

func validAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	p, parseErr := strconv.Atoi(port)
	return err == nil && parseErr == nil && host == "127.0.0.1" && p > 0 && p <= 65535
}

func readInstanceAddress(path string) string {
	data, err := os.ReadFile(path)
	var record instanceRecord
	if err != nil || json.Unmarshal(data, &record) != nil || !validAddress(record.Address) || record.PID == 0 {
		return ""
	}
	// A crash may leave a record whose port now belongs to another process.
	owner := PortOwner(record.Address)
	if owner == nil || owner.PID != record.PID {
		return ""
	}
	return record.Address
}

func (i *Instance) Publish(address string) error {
	if !validAddress(address) {
		return fmt.Errorf("无效的控制台地址：%s", address)
	}
	if err := os.MkdirAll(filepath.Dir(i.path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(instanceRecord{Address: address, PID: uint32(os.Getpid())})
	if err != nil {
		return err
	}
	// Readers retry incomplete writes while waiting for the owner to be ready.
	return os.WriteFile(i.path, data, 0600)
}

func (i *Instance) clear() error {
	err := os.Remove(i.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (i *Instance) Close() error {
	i.once.Do(func() {
		// Remove the record before releasing ownership to the next instance.
		i.err = errors.Join(i.clear(), i.lease.Close())
	})
	return i.err
}
