// Package legal contains the single source of the distributed usage notice.
package legal

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
)

const Version = "2026-10-02.1"

//go:embed TERMS.txt
var Text string

func Hash() string {
	sum := sha256.Sum256([]byte(Text))
	return hex.EncodeToString(sum[:])
}

func CheckAcceptance(version string) error {
	if version != Version {
		return fmt.Errorf("请先使用 -show-terms 阅读条款；同意后显式传入 -accept-terms=%s（软件许可不代表视频内容授权）", Version)
	}
	return nil
}
