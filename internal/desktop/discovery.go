package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// Instance owns the discovery record for as long as it owns the desktop mutex.
type Instance struct {
	lease    Lease
	path     string
	once     sync.Once
	err      error
	identity Identity
}

type instanceRecord struct {
	Address  string   `json:"address"`
	Identity Identity `json:"identity"`
}

func validAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	p, parseErr := strconv.Atoi(port)
	return err == nil && parseErr == nil && host == "127.0.0.1" && p > 0 && p <= 65535
}

func readInstanceRecord(path string) *instanceRecord {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 64<<10))
	var record instanceRecord
	if err != nil || json.Unmarshal(data, &record) != nil || !validAddress(record.Address) || !record.Identity.valid() {
		return nil
	}
	// A crash may leave a record whose port now belongs to another process.
	owner := PortOwner(record.Address)
	if owner == nil || owner.PID != record.Identity.PID {
		return nil
	}
	exe, started, err := processIdentity(owner.PID)
	if err != nil || started != record.Identity.Started || !samePath(exe, record.Identity.Executable) {
		return nil
	}
	return &record
}

func (i *Instance) Publish(address string) error {
	if !validAddress(address) || !i.identity.valid() {
		return fmt.Errorf("无效的控制台地址：%s", address)
	}
	if err := os.MkdirAll(filepath.Dir(i.path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(instanceRecord{Address: address, Identity: i.identity})
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
