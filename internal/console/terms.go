package console

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"still-wanna-dance/internal/legal"
	"still-wanna-dance/internal/workfiles"
)

type termsReceipt struct {
	Version    string    `json:"version"`
	Hash       string    `json:"sha256"`
	AcceptedAt time.Time `json:"acceptedAt"`
}

type dataProtection struct {
	protect, unprotect func([]byte) ([]byte, error)
}

// Tests on non-Windows hosts replace this before any consoles are constructed.
// Production never falls back to plaintext or a locally managed key.
var defaultTermsProtection = dataProtection{protectUserData, unprotectUserData}

type termsEnvelope struct {
	Format     int    `json:"format"`
	Protection string `json:"protection"`
	Ciphertext []byte `json:"ciphertext"`
}

type protectedTerms struct {
	Purpose string `json:"purpose"`
	termsReceipt
}

const termsPurpose = "swd-terms-receipt-v1"
const maxTermsBytes = 64 * 1024

func (r termsReceipt) valid() bool {
	// A local clock is not proof of identity. Reject zero/out-of-range times;
	// allow later clock corrections without expiring an otherwise valid receipt.
	return r.Version == legal.Version && r.Hash == legal.Hash() &&
		!r.AcceptedAt.IsZero() && r.AcceptedAt.Year() >= 1 && r.AcceptedAt.Year() <= 9999
}

func (c *Console) termsAccepted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.terms.valid()
}

func (c *Console) loadTerms() error {
	c.terms = termsReceipt{}
	c.termsNeedsConfirmation = false
	c.termsProtection = defaultTermsProtection
	f, err := os.Open(c.files().Terms)
	if os.IsNotExist(err) {
		return nil
	}
	c.termsNeedsConfirmation = true
	if err != nil {
		return fmt.Errorf("读取条款确认记录：%w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxTermsBytes+1))
	if err != nil {
		return fmt.Errorf("读取条款确认记录：%w", err)
	}
	var envelope termsEnvelope
	if len(b) > maxTermsBytes || json.Unmarshal(b, &envelope) != nil || envelope.Format != 1 ||
		envelope.Protection != "dpapi-current-user" || len(envelope.Ciphertext) == 0 {
		slog.Warn("terms_receipt_invalid", "reason", "unsupported or corrupt format")
		return nil
	}
	plain, err := c.termsProtection.unprotect(envelope.Ciphertext)
	if err != nil {
		slog.Warn("terms_receipt_invalid", "reason", "current user decryption failed")
		return nil
	}
	defer clear(plain)
	var receipt protectedTerms
	if json.Unmarshal(plain, &receipt) != nil || receipt.Purpose != termsPurpose || !receipt.termsReceipt.valid() {
		slog.Warn("terms_receipt_invalid", "reason", "invalid or outdated terms")
		return nil
	}
	c.terms = receipt.termsReceipt
	c.termsNeedsConfirmation = false
	return nil
}

func (c *Console) acceptTerms() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return fmt.Errorf("程序正在退出")
	}
	receipt := termsReceipt{legal.Version, legal.Hash(), time.Now().UTC()}
	plain, err := json.Marshal(protectedTerms{termsPurpose, receipt})
	if err != nil {
		return err
	}
	defer clear(plain)
	cipher, err := c.termsProtection.protect(plain)
	if err != nil {
		return err
	}
	b, err := json.Marshal(termsEnvelope{1, "dpapi-current-user", cipher})
	if err != nil {
		return err
	}
	if err = workfiles.Write(c.files().Terms, b); err != nil {
		return err
	}
	c.terms = receipt
	c.termsNeedsConfirmation = false
	c.cancelTermsExitLocked()
	return c.startMonitorLocked()
}

// Called with mu held; Stop alone cannot invalidate a callback waiting for mu.
func (c *Console) cancelTermsExitLocked() {
	c.termsExitGeneration++
	if c.termsExitTimer != nil {
		c.termsExitTimer.Stop()
		c.termsExitTimer = nil
	}
}

func (c *Console) termsPagePresence(pageID string, leaving bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing || pageID == "" || pageID != c.termsPageID ||
		c.terms.valid() {
		return
	}
	c.cancelTermsExitLocked()
	if !leaving {
		return
	}
	generation := c.termsExitGeneration
	c.termsExitTimer = time.AfterFunc(time.Minute, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closing || generation != c.termsExitGeneration {
			return
		}
		c.termsExitTimer = nil
		c.closing = true // serialize timeout with terms acceptance
		c.exitOnce.Do(func() {
			slog.Info("terms_page_timeout")
			close(c.exitRequested)
		})
	})
}

// The gate precedes all application routes, including read APIs that can start work.
type termsEntry struct {
	PageID string `json:"pageID"`
}

func (c *Console) serveConsolePage(w http.ResponseWriter, entry *termsEntry) {
	c.serveWebUI(w, "console", "WebUI", map[string]any{"termsGate": entry})
}

func (c *Console) serveTerms(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == "GET" && r.URL.Path == "/api/terms/current" {
		c.mu.Lock()
		accepted, needsConfirmation := c.terms.valid(), c.termsNeedsConfirmation
		c.mu.Unlock()
		writeJSON(w, map[string]any{"text": legal.Text, "version": legal.Version, "sha256": legal.Hash(), "accepted": accepted, "needsConfirmation": needsConfirmation})
		return true
	}
	if r.Method == "GET" && r.URL.Path == "/terms.txt" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, legal.Text)
		return true
	}
	if r.Method == "GET" && (r.URL.Path == "/terms" || (r.URL.Path == "/" && !c.termsAccepted())) {
		if c.termsAccepted() {
			c.serveWebUI(w, "terms", "使用条款", nil)
			return true
		}
		c.mu.Lock()
		c.cancelTermsExitLocked()
		c.termsPageID = rand.Text()
		pageID := c.termsPageID
		c.mu.Unlock()
		c.serveConsolePage(w, &termsEntry{PageID: pageID})
		return true
	}
	if r.URL.Path == "/api/terms/leave" || r.URL.Path == "/api/terms/return" {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return true
		}
		if r.Header.Get("X-SWD-Token") != c.token || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+c.address) {
			http.Error(w, "invalid origin or token", 403)
			return true
		}
		var input struct {
			PageID string `json:"pageID"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&input) != nil || input.PageID == "" {
			http.Error(w, "invalid page", 400)
			return true
		}
		c.termsPagePresence(input.PageID, r.URL.Path == "/api/terms/leave")
		writeJSON(w, map[string]bool{"ok": true})
		return true
	}
	if r.URL.Path == "/api/terms/accept" {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return true
		}
		if r.Header.Get("X-SWD-Token") != c.token || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+c.address) {
			http.Error(w, "invalid origin or token", 403)
			return true
		}
		var input struct {
			Version       string `json:"version"`
			Hash          string `json:"sha256"`
			Agree         bool   `json:"agree"`
			ContentRights bool   `json:"contentRights"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&input) != nil || !input.Agree || !input.ContentRights || input.Version != legal.Version || input.Hash != legal.Hash() {
			http.Error(w, "请重新阅读当前条款并主动确认同意及内容授权声明", 400)
			return true
		}
		if err := c.acceptTerms(); err != nil {
			slog.Error("terms_accept_failed", "error", err)
			http.Error(w, "无法安全保存同意记录：需要 Windows 当前用户保护及可写的配置目录，请检查运行环境和日志后重试", 500)
			return true
		}
		writeJSON(w, map[string]bool{"ok": true})
		// Consent is already durable. Do not hold the response open during UAC.
		go c.autoStart()
		return true
	}
	if !c.termsAccepted() && r.URL.Path != "/api/identity" && r.URL.Path != "/api/stop" && r.URL.Path != "/api/queue/stop" && r.URL.Path != "/api/exit" && r.URL.Path != "/api/restart" && r.URL.Path != "/api/hosts/disable" && !strings.HasPrefix(r.URL.Path, "/assets/") && r.URL.Path != "/about" && r.URL.Path != "/help" {
		http.Error(w, "请先打开 WebUI 阅读并同意使用条款", http.StatusPreconditionRequired)
		return true
	}
	return false
}
