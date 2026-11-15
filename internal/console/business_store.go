package console

import (
	"errors"

	"still-wanna-dance/internal/cacheproxy"
)

// The pointer and opening result are fixed for this Console. Settings changes
// require restart; no business operation retries creation or changes ownership.
func (c *Console) openedBusinessStore() (*cacheproxy.Store, error) {
	if c.store == nil {
		if c.storageOpenError != nil {
			return nil, c.storageOpenError
		}
		if c.storageConfirmation != nil {
			return nil, c.storageConfirmation
		}
		return nil, errors.New("数据库尚未打开，请在设置中打开已有数据库或明确新建")
	}
	return c.store, nil
}

func (c *Console) businessStore() (*cacheproxy.Store, error) {
	store, err := c.openedBusinessStore()
	if err != nil {
		return nil, err
	}
	if err := store.Error(); err != nil {
		return nil, err
	}
	return store, nil
}
