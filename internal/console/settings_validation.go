package console

import (
	"errors"
	"net/http"
)

// Field attribution is independent of severity: invalid drafts are expected
// rejections, while failures of active storage retain their operational cause.
type settingsValidationError struct {
	Fields map[string]string
	cause  error
}

func (e *settingsValidationError) Error() string { return e.cause.Error() }
func (e *settingsValidationError) Unwrap() error { return e.cause }

func invalidSetting(err error, fields ...string) error {
	if err == nil {
		return nil
	}
	return withSettingFields(rejectRequest(err), fields...)
}

func withSettingFields(err error, fields ...string) error {
	if err == nil {
		return nil
	}
	messages := make(map[string]string, len(fields))
	for _, field := range fields {
		messages[field] = err.Error()
	}
	return &settingsValidationError{Fields: messages, cause: err}
}

func storageSettingField(choice string) string {
	switch choice {
	case "database":
		return "databasePath"
	case "video":
		return "videoDir"
	default:
		return "blockCacheDir"
	}
}

func writeSettingsValidation(w http.ResponseWriter, err error) bool {
	var validation *settingsValidationError
	if !errors.As(err, &validation) {
		return false
	}
	preserveResponseFault(w, err)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	writeJSON(w, map[string]any{"code": "settings_validation", "error": err.Error(), "fields": validation.Fields})
	return true
}
