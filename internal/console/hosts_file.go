package console

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"still-wanna-dance/internal/workfiles"
)

// System hosts has its own permissions and external writers; the configuration
// lease and disposable-work-slot contract of workfiles.Write do not apply here.
// Never truncate hosts, including during rollback or helper-disconnect cleanup.
func replaceHostsFile(path string, before []byte, contents io.Reader) error {
	stage, err := stageHostsFile(path, before)
	if err != nil {
		return err
	}
	defer os.Remove(stage.Name())
	defer stage.Close()
	if _, err := io.Copy(stage, contents); err != nil {
		return err
	}
	if err := stage.Sync(); err != nil {
		return err
	}
	if err := stage.Close(); err != nil {
		return err
	}
	if err := saveHostsBackup(path, before); err != nil {
		return err
	}
	// An unrelated editor may have changed hosts while the replacement was
	// being prepared. Do not replace that content with our earlier snapshot.
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, before) {
		return errors.New("hosts 在操作期间被修改，原文件未替换，请重试")
	}
	return commitHostsFile(stage.Name(), path)
}

// Preparing recovery uses the same access and staging checks without changing
// hosts or creating a backup. The caller always closes and removes its stage.
func stageHostsFile(path string, before []byte) (*os.File, error) {
	if err := workfiles.CheckDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !workfiles.Regular(info) {
		return nil, errors.New("hosts 必须是普通文件，原文件未修改")
	}
	original, err := openHostsFile(path)
	if err != nil {
		return nil, err
	}
	defer original.Close()
	actual, err := original.Stat()
	if err != nil {
		return nil, err
	}
	if !workfiles.Regular(actual) || !os.SameFile(info, actual) {
		return nil, errors.New("hosts 在操作期间被替换，原文件未修改，请重试")
	}
	current, err := io.ReadAll(original)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(current, before) {
		return nil, errors.New("hosts 在操作期间被修改，原文件未替换，请重试")
	}
	stage, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".swd-*")
	if err != nil {
		return nil, err
	}
	if err := preserveHostsPermissions(original, stage); err != nil {
		stage.Close()
		os.Remove(stage.Name())
		return nil, fmt.Errorf("保留 hosts 权限：%w", err)
	}
	return stage, nil
}

func saveHostsBackup(path string, before []byte) (err error) {
	// Keep the first snapshot for manual inspection. Recovery always uses the
	// current operation's snapshot, never this potentially much older backup.
	backup, err := os.OpenFile(path+".swd-backup", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		backup.Close()
		if err != nil {
			os.Remove(backup.Name())
		}
	}()
	if _, err = backup.Write(before); err != nil {
		return err
	}
	if err = backup.Sync(); err != nil {
		return err
	}
	return backup.Close()
}
