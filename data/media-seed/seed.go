// Package mediaseed contains a reproducible, URL-free bootstrap snapshot.
package mediaseed

import _ "embed"

type Mapping struct {
	ID  int64  `json:"id"`
	MD5 string `json:"md5"`
}

type Media struct {
	MD5      string `json:"md5"`
	ByteSize *int64 `json:"byteSize,omitempty"`
}

type Source struct {
	Name     string `json:"name"`
	Revision string `json:"revision"`
	SHA256   string `json:"sha256"`
}

type Snapshot struct {
	Version   string    `json:"version"`
	Sources   []Source  `json:"sources"`
	Media     []Media   `json:"media"`
	SongMedia []Mapping `json:"songMedia"`
}

//go:embed media-seed.json
var bundled string

func Bundled() string { return bundled }
