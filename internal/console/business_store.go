package console

import (
	"errors"
	"fmt"

	"still-wanna-dance/internal/cacheproxy"
)

// The pointer and opening result are fixed for this Console. Settings changes
// require restart; no business operation retries creation or changes ownership.
func (c *Console) businessStore() (*cacheproxy.Store, error) {
	if c.store == nil {
		if c.storeOpenError == nil {
			return nil, errors.New("数据库尚未打开，请在设置中打开已有数据库或明确新建")
		}
		return nil, fmt.Errorf("数据库不可用，请在设置中打开已有数据库或明确新建：%w", c.storeOpenError)
	}
	if err := c.store.Error(); err != nil {
		return nil, err
	}
	return c.store, nil
}
