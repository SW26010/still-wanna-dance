package apppaths

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// User-selected data must not overlap the disposable work tree. Call before
// acquiring/cleaning a workspace, including when the config itself is external.
func ValidateWorkSeparation(executable, path string) error {
	work := filepath.Join(filepath.Dir(executable), "work")
	contains := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	if contains(work, path) || contains(path, work) {
		return fmt.Errorf("数据位置不能与程序临时工作目录重叠：%s", work)
	}
	return nil
}

// Callers pass canonical absolute paths, just as for ForConfig. Work always
// belongs to the executable's volume, even with an external configuration.
func WorkForConfig(executable, config string) string {
	return workPath(executable, "console", config)
}

// CLI instances have no configuration file. Their selected database/video pair
// identifies the work owner; a desktop instance never shares this namespace.
func WorkForService(executable, database, videos string) string {
	return workPath(executable, "service", database, videos)
}

func workPath(executable, kind string, identities ...string) string {
	base := filepath.Dir(executable)
	h := sha256.New()
	for _, identity := range identities {
		identity = filepath.Clean(identity)
		rel, err := filepath.Rel(base, identity)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			// Moving an entire portable installation keeps the same scope.
			identity = "relative:" + rel
		} else {
			identity = "absolute:" + identity
		}
		identity = filepath.ToSlash(identity)
		if runtime.GOOS == "windows" {
			identity = strings.ToLower(identity)
		}
		h.Write([]byte(identity))
		h.Write([]byte{0})
	}
	return filepath.Join(base, "work", kind+"-"+hex.EncodeToString(h.Sum(nil)[:16]))
}
