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
	Format     int               `json:"format"`
	Protection string            `json:"protection"`
	Receipts   []json.RawMessage `json:"receipts"` // independent base64 ciphertexts, oldest first
}

type protectedTerms struct {
	Purpose string `json:"purpose"`
	termsReceipt
}

const termsPurpose = "swd-terms-receipt-v1"
const maxTermsBytes = 64 * 1024
const maxTermsReceipts = 8

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
	receipts, present, err := readTermsReceipts(c.files().Terms)
	if err != nil {
		return err
	}
	c.terms = c.findAcceptedTerms(receipts)
	c.termsNeedsConfirmation = present && !c.terms.valid()
	if len(receipts) > 0 && c.termsNeedsConfirmation {
		slog.Warn("terms_receipt_invalid", "reason", "no verifiable current terms receipt")
	}
	return nil
}

// Invalid envelopes remain untouched until explicit consent replaces them.
// Individual slots stay opaque so another user's receipt survives new consent.
func readTermsReceipts(path string) ([]json.RawMessage, bool, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, fmt.Errorf("读取条款确认记录：%w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxTermsBytes+1))
	if err != nil {
		return nil, true, fmt.Errorf("读取条款确认记录：%w", err)
	}
	var envelope termsEnvelope
	if len(b) > maxTermsBytes || json.Unmarshal(b, &envelope) != nil || envelope.Format != 2 ||
		envelope.Protection != "dpapi-current-user" || len(envelope.Receipts) == 0 || len(envelope.Receipts) > maxTermsReceipts {
		slog.Warn("terms_receipt_invalid", "reason", "unsupported or corrupt format")
		return nil, true, nil
	}
	return envelope.Receipts, true, nil
}

func (c *Console) findAcceptedTerms(receipts []json.RawMessage) termsReceipt {
	for i := len(receipts) - 1; i >= 0; i-- {
		var cipher []byte
		if json.Unmarshal(receipts[i], &cipher) != nil || len(cipher) == 0 {
			continue
		}
		plain, err := c.termsProtection.unprotect(cipher)
		if err != nil {
			continue
		}
		var receipt protectedTerms
		err = json.Unmarshal(plain, &receipt)
		clear(plain)
		if err == nil && receipt.Purpose == termsPurpose && receipt.termsReceipt.valid() {
			return receipt.termsReceipt
		}
	}
	return termsReceipt{}
}

func (c *Console) acceptTerms() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return fmt.Errorf("程序正在退出")
	}
	receipts, _, err := readTermsReceipts(c.files().Terms)
	if err != nil {
		return err
	}
	receipt := c.findAcceptedTerms(receipts)
	if !receipt.valid() {
		receipt = termsReceipt{legal.Version, legal.Hash(), time.Now().UTC()}
		plain, err := json.Marshal(protectedTerms{termsPurpose, receipt})
		if err != nil {
			return err
		}
		defer clear(plain)
		cipher, err := c.termsProtection.protect(plain)
		if err != nil {
			return err
		}
		slot, err := json.Marshal(cipher)
		if err != nil {
			return err
		}
		if len(receipts) == maxTermsReceipts {
			receipts = receipts[1:]
		}
		receipts = append(receipts, slot)
		b, err := json.Marshal(termsEnvelope{2, "dpapi-current-user", receipts})
		if err != nil {
			return err
		}
		if len(b) > maxTermsBytes {
			return fmt.Errorf("条款确认记录超过 %d 字节上限", maxTermsBytes)
		}
		if err = workfiles.Write(c.files().Terms, b); err != nil {
			return err
		}
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
