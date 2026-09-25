package console

import (
	"crypto/md5"
	"crypto/sha256"
	"fmt"
	"path/filepath"
)

func fixtureVideoPath(root, id, body string) string {
	key := sha256.Sum256([]byte(fmt.Sprintf("%s/abc/%x/%d", id, md5.Sum([]byte(body)), len(body))))
	return filepath.Join(root, "videos", fmt.Sprintf("%x.mp4", key))
}
