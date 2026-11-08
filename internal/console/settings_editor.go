package console

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"

	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/desktop"
)

// settingsEditor owns configuration editing independently of running services.
// The application supplies its lifecycle transaction lock so every config writer
// and hosts operation keeps the existing ordering. Snapshot readers use mu only.
type settingsEditor struct {
	mu                              sync.Mutex
	lifecycleMu                     *sync.Mutex
	identity                        desktop.Identity
	configPath                      string
	workPath                        string
	settings, savedSettings         Settings
	loadedNodeCount, savedNodeCount int
	proxyImports                    map[string]proxyImportDraft
	hosts                           *hostsController
	closing                         bool
	pathPickerCancel                context.CancelFunc
	pathPicker                      func(context.Context, desktop.PathDialog) (string, error)
	termsAccepted                   func() bool
	// Publish saved and immediately active values as one application snapshot.
	// Called without mu; the publisher acquires its state lock before commit.
	onSaved      func(Settings, func())
	onResult     func(string, error)
	actionErrors map[string]string
}

func newManagementToken() (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

func newSettingsEditor(identity desktop.Identity, transaction *sync.Mutex) *settingsEditor {
	if transaction == nil {
		transaction = new(sync.Mutex)
	}
	return &settingsEditor{identity: identity, configPath: identity.Config, lifecycleMu: transaction,
		hosts: newHostsController(hostsPath(), launchHostsSession)}
}

func (c *settingsEditor) files() apppaths.Paths { return apppaths.ForConfig(c.configPath) }
func (c *settingsEditor) snapshot() Settings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.savedSettings
}
func (c *settingsEditor) reload(s Settings) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.savedSettings = s
	c.savedNodeCount = configurationNodeCount(s.nodeSnapshot)
}
func (c *settingsEditor) admitted(requireTerms bool) bool {
	return !requireTerms || c.termsAccepted != nil && c.termsAccepted()
}
func (c *settingsEditor) beginShutdown() {
	c.mu.Lock()
	c.closing = true
	if c.pathPickerCancel != nil {
		c.pathPickerCancel()
	}
	c.mu.Unlock()
	c.hosts.cancel()
}
func (c *settingsEditor) writeActionResult(w http.ResponseWriter, r *http.Request, source string, err error) {
	if c.onResult != nil {
		c.onResult(source, err)
	} else if !nonFaultError(err) {
		c.mu.Lock()
		if c.actionErrors == nil {
			c.actionErrors = make(map[string]string)
		}
		if err == nil {
			delete(c.actionErrors, source)
		} else {
			c.actionErrors[source] = err.Error()
		}
		c.mu.Unlock()
	}
	writeActionResult(w, r, err)
}

func (c *settingsEditor) initializeSettings(s Settings) {
	c.settings, c.savedSettings = s, s
	c.loadedNodeCount = configurationNodeCount(s.nodeSnapshot)
	c.savedNodeCount = c.loadedNodeCount
}

// This is the single list of configuration operations, shared by the normal
// router and recovery. Returning nil keeps business operations out of recovery.
// Management authentication remains the caller's responsibility; consent is
// required by the normal application but not for repairing configuration.
func (c *settingsEditor) settingsHandler(path string, requireTerms bool) http.HandlerFunc {
	var handler http.HandlerFunc
	switch path {
	case "/api/proxy/import":
		// Import owns its larger body limit.
		return func(w http.ResponseWriter, r *http.Request) { c.serveProxyImportAuthorized(w, r, requireTerms) }
	case "/api/path/select":
		handler = func(w http.ResponseWriter, r *http.Request) { c.servePathPickerAuthorized(w, r, requireTerms) }
	case "/api/storage/check", "/api/storage/validate-location", "/api/storage/create-database", "/api/storage/prepare-database":
		handler = c.serveStorage
	case "/api/settings", "/api/settings/theme", "/api/proxy/import/discard":
		handler = c.serveSettingsEdit
	default:
		return nil
	}
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		handler(w, r)
	}
}

func (c *settingsEditor) serveSettingsEdit(w http.ResponseWriter, r *http.Request) {
	var err error
	source := "settings"
	switch r.URL.Path {
	case "/api/settings":
		var input json.RawMessage
		if err = decodeRequest(json.NewDecoder(r.Body), &input); err == nil {
			err = c.saveSettingsPatch(input)
		}
	case "/api/settings/theme":
		var input struct {
			Theme string `json:"theme"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err = decodeRequest(decoder, &input); err == nil {
			err = c.saveTheme(input.Theme)
		}
	case "/api/proxy/import/discard":
		source = "proxy-import"
		var input struct {
			ID string `json:"id"`
		}
		if err = decodeRequest(json.NewDecoder(r.Body), &input); err == nil {
			c.mu.Lock()
			delete(c.proxyImports, input.ID)
			c.mu.Unlock()
			// Discard is idempotent and does not clear a prior import error.
			writeJSON(w, map[string]bool{"ok": true})
			return
		}
	}
	c.writeActionResult(w, r, source, err)
}
