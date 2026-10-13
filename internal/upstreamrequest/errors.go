package upstreamrequest

import "errors"

// These failures describe local execution, not remote node quality. They must
// survive net/http wrapping; real network and request deadlines remain failures.
var (
	ErrCapacity = errors.New("upstream local connection capacity exhausted")
	ErrRetired  = errors.New("upstream runtime retired")
)
