package console

// Pause queue prefetch while downloading the library. Lifecycle serialization
// prevents CDN start/stop or settings saves from interleaving with the switch.
func (c *Console) switchTask() error {
	c.taskMu.Lock()
	defer c.taskMu.Unlock()
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	var done chan struct{}
	if c.queueCancel != nil {
		c.queueCancel()
		done = c.queueDone
	}
	c.mu.Unlock()
	if done != nil {
		<-done
	}
	err := c.startBatchModeLocked(false)
	if err != nil {
		c.resumeQueueLocked()
	}
	return err
}
