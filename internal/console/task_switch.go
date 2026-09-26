package console

import "errors"

// Switching is explicit in the button label. Wait for the previous worker to
// exit before starting its replacement; shared playback downloads stay alive.
func (c *Console) switchTask(batch bool) error {
	c.taskMu.Lock()
	defer c.taskMu.Unlock()
	c.mu.Lock()
	var done chan struct{}
	resumeQueue := false
	if batch && c.queueCancel != nil {
		resumeQueue = true
		c.queueCancel()
		done = c.queueDone
	} else if !batch && c.batchCancel != nil && !c.batch.ScanOnly {
		c.batchCancel()
		done = c.batchDone
	}
	c.mu.Unlock()
	if done != nil {
		<-done
	}
	if batch {
		if err := c.startBatchCheckResume(false, false, resumeQueue); err != nil {
			if resumeQueue {
				resumeErr := c.startQueue()
				c.recordActionError("/api/queue/start", resumeErr)
				return errors.Join(err, resumeErr)
			}
			return err
		}
		return nil
	}
	return c.startQueue()
}
