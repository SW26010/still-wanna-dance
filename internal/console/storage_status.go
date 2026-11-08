package console

import (
	"errors"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/cacheproxy"
)

type storageFieldIssue struct {
	Message  string `json:"message"`
	Severity string `json:"severity"`
	Value    string `json:"value"`
}

// Project known runtime results onto the existing settings fields. Bind each
// issue to its active path so editing or saving a replacement cannot label that
// new choice with the old instance's failure. This performs no filesystem I/O.
func (c *Console) storageSettingsIssues(active Settings, service *cacheproxy.Server) map[string]storageFieldIssue {
	issues := map[string]storageFieldIssue{}
	values := map[string]string{"databasePath": active.DatabasePath, "videoDir": active.VideoDir, "blockCacheDir": active.BlockCacheDir}
	add := func(err error, severity string, fields ...string) {
		if err == nil {
			return
		}
		for _, field := range fields {
			if previous, ok := issues[field]; ok && previous.Severity == "error" {
				continue
			}
			issues[field] = storageFieldIssue{applog.SafeError(err).Error(), severity, values[field]}
		}
	}
	if c.storageConfirmation != nil {
		for _, field := range []string{"databasePath", "videoDir"} {
			if values[field] == "" {
				add(c.storageConfirmation, "warning", field)
			}
		}
	}
	var visit func(error)
	visit = func(err error) {
		if err == nil {
			return
		}
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				visit(child)
			}
			return
		}
		var validation *settingsValidationError
		if errors.As(err, &validation) {
			for field, message := range validation.Fields {
				add(errors.New(message), "error", field)
			}
		} else {
			add(err, "error", "databasePath", "videoDir")
		}
	}
	visit(c.storageOpenError)
	add(c.workOpenError, "error", "blockCacheDir")
	if c.store != nil {
		add(c.store.Error(), "error", "databasePath")
	}
	if service != nil {
		select {
		case <-service.LocalFailureDone():
			// The engine's retained media error may concern either video root;
			// do not falsely attribute it exclusively to the database.
			add(service.StorageError(), "error", "videoDir", "blockCacheDir")
		default:
		}
	}
	return issues
}
