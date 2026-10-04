package console

import (
	"crypto/md5"
	"fmt"
	"path/filepath"
)

func fixtureVideoPath(root, id, body string) string {
	key := md5.Sum([]byte(body))
	return filepath.Join(root, "videos", fmt.Sprintf("%x.mp4", key))
}
