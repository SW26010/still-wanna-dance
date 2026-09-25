package console

// Switching is explicit in the button label. Wait for the previous worker to
// exit before starting its replacement; shared playback downloads stay alive.
func (c *Console) switchTask(batch bool) error {
	c.taskMu.Lock()
	defer c.taskMu.Unlock()
	c.mu.Lock()
	var done chan struct{}
	if batch && c.queueCancel != nil {
		c.queueCancel()
		done = c.queueDone
	} else if !batch && c.batchCancel != nil {
		c.batchCancel()
		done = c.batchDone
	}
	c.mu.Unlock()
	if done != nil {
		<-done
	}
	if batch {
		return c.startBatch()
	}
	return c.startQueue()
}
