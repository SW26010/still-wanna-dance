package console

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"still-wanna-dance/internal/desktop"
)

// Both startup paths build the same configuration editor before adding any
// running services. Settings handlers depend only on this baseline, not on a
// store, download engine or network runtime.
func newSettingsConsole(address string, identity desktop.Identity) (*Console, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	return &Console{
		address: address, identity: identity, configPath: identity.Config,
		token: hex.EncodeToString(token[:]), lifecycle: newApplicationLifecycle(),
	}, nil
}

func (c *Console) initializeSettings(s Settings) {
	c.settings, c.savedSettings = s, s
	c.loadedNodeCount = configurationNodeCount(s.nodeSnapshot)
	c.savedNodeCount = c.loadedNodeCount
	c.hosts = newHostsController(hostsPath(), launchHostsSession)
}

// This is the single list of configuration operations, shared by the normal
// router and recovery. Returning nil keeps business operations out of recovery.
// Management authentication remains the caller's responsibility; consent is
// required by the normal application but not for repairing configuration.
func (c *Console) settingsHandler(path string, requireTerms bool) http.HandlerFunc {
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

func (c *Console) serveSettingsEdit(w http.ResponseWriter, r *http.Request) {
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
