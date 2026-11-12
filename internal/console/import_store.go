package console

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/workfiles"
)

const importStoreApplicationID = 0x53574449 // SWDI; independent of the media library.

// A connection belongs to one worker. The configuration lease excludes other
// writers; no state database connection survives a paused or completed worker.
func connectImportStore(path, mode string) (*sql.DB, error) {
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	q := url.Values{"mode": {mode}, "_pragma": {"busy_timeout(1000)", "synchronous(FULL)"}}
	uri := url.URL{Scheme: "file", Path: uriPath, RawQuery: q.Encode()}
	db, err := sql.Open("sqlite", uri.String())
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}

func checkImportStoreFiles(path string) error {
	if err := workfiles.CheckDir(filepath.Dir(path)); err != nil {
		return err
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_, err := os.Lstat(path + suffix)
		if os.IsNotExist(err) && suffix != "" {
			continue
		}
		if err != nil {
			return err
		}
		if err := cacheproxy.CheckDatabaseFile(path + suffix); err != nil {
			return err
		}
	}
	return nil
}

func checkImportCompanionsAbsent(path string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); err == nil {
			return fmt.Errorf("导入记录恢复文件仍存在，不能创建或替换主文件：%s", path+suffix)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func openImportStore(path string, write bool) (*sql.DB, error) {
	if err := checkImportStoreFiles(path); err != nil {
		return nil, err
	}
	mode := "ro"
	if write {
		mode = "rw"
	}
	db, err := connectImportStore(path, mode)
	if err != nil {
		return nil, err
	}
	var identity, version int
	if err = db.QueryRow("PRAGMA application_id").Scan(&identity); err == nil {
		err = db.QueryRow("PRAGMA user_version").Scan(&version)
	}
	if err == nil && (identity != importStoreApplicationID || version != 1) {
		err = errors.New("不支持的导入记录格式")
	}
	if err == nil && write {
		_, err = db.Exec("PRAGMA journal_mode=WAL")
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Full snapshots are written only at creation/enumeration and legacy migration.
// Build an unpublished database with an in-memory rollback journal, then sync
// and atomically publish it through the same fixed work slot as JSON writers.
// A crash leaves only that disposable slot, never a half-migrated formal file.
func writeImportSnapshot(path string, task videoImportTask) error {
	dir := filepath.Dir(path)
	if err := workfiles.CheckFutureDir(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		db, err := openImportStore(path, true)
		if err != nil {
			return err
		}
		if err := db.Close(); err != nil {
			return err
		}
		// Never replace a main file while SQLite recovery companions remain,
		// for example if another reader prevented the final checkpoint.
		if err := checkImportCompanionsAbsent(path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	} else {
		if err := checkImportCompanionsAbsent(path); err != nil {
			return err
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	tmp := workfiles.TempName(path)
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	if err := f.Close(); err != nil {
		return err
	}
	db, err := connectImportStore(filepath.Join(dir, tmp), "rw")
	if err != nil {
		return err
	}
	err = initializeImportStore(db, task)
	err = errors.Join(err, db.Close())
	if err != nil {
		return err
	}
	f, err = root.OpenFile(tmp, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = errors.Join(f.Sync(), f.Close())
	if err != nil {
		return err
	}
	return root.Rename(tmp, filepath.Base(path))
}

// Explicit discard is allowed even for a damaged record. Workers have stopped
// and closed their connections; remove only this state's known regular files.
func (c *Console) removeImportState() error {
	if err := removeLegacyImport(c.files().State("video-import")); err != nil {
		return err
	}
	path := c.files().VideoImport
	if err := workfiles.CheckDir(filepath.Dir(path)); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	for _, suffix := range []string{"-wal", "-shm", "-journal", ""} {
		if err := workfiles.RemoveFile(root, filepath.Base(path)+suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func initializeImportStore(db *sql.DB, task videoImportTask) error {
	if _, err := db.Exec("PRAGMA journal_mode=MEMORY; PRAGMA synchronous=OFF"); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA application_id=%d; PRAGMA user_version=1;
CREATE TABLE task (singleton INTEGER PRIMARY KEY CHECK(singleton=1), metadata TEXT NOT NULL, file_count INTEGER NOT NULL);
CREATE TABLE items (position INTEGER PRIMARY KEY, data TEXT NOT NULL);`, importStoreApplicationID)); err != nil {
		return err
	}
	count := len(task.Files)
	files := task.Files
	task.Files = nil
	metadata, err := json.Marshal(task)
	if err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO task VALUES (1, ?, ?)", string(metadata), count); err != nil {
		return err
	}
	stmt, err := tx.Prepare("INSERT INTO items VALUES (?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, item := range files {
		data, err := json.Marshal(item)
		if err != nil {
			return err
		}
		if _, err := stmt.Exec(i, string(data)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func readImportStore(path string) (videoImportTask, error) {
	var task videoImportTask
	db, err := openImportStore(path, false)
	if err != nil {
		return task, err
	}
	defer db.Close()
	var data string
	var count int
	if err := db.QueryRow("SELECT metadata, file_count FROM task WHERE singleton=1").Scan(&data, &count); err != nil {
		return task, err
	}
	if err := json.Unmarshal([]byte(data), &task); err != nil {
		return task, err
	}
	if len(task.Files) != 0 || count < 0 {
		return task, errors.New("导入清单不完整")
	}
	rows, err := db.Query("SELECT position, data FROM items ORDER BY position")
	if err != nil {
		return task, err
	}
	defer rows.Close()
	for rows.Next() {
		var index int
		if err := rows.Scan(&index, &data); err != nil {
			return task, err
		}
		if index != len(task.Files) || index >= count {
			return task, errors.New("导入清单顺序或数量不完整")
		}
		var item videoImportItem
		if err := json.Unmarshal([]byte(data), &item); err != nil {
			return task, err
		}
		task.Files = append(task.Files, item)
	}
	if err := rows.Err(); err != nil {
		return task, err
	}
	if len(task.Files) != count {
		return task, errors.New("导入清单不完整")
	}
	return task, task.validate()
}

type importItemUpdate struct {
	index int
	item  videoImportItem
}

func saveImportItems(db *sql.DB, updates []importItemUpdate) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare("UPDATE items SET data=? WHERE position=?")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, update := range updates {
		data, err := json.Marshal(update.item)
		if err != nil {
			return err
		}
		result, err := stmt.Exec(string(data), update.index)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("导入清单不完整，条目不存在：%d", update.index)
		}
	}
	return tx.Commit()
}
