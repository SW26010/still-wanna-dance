package console

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/upstreamcheck"
)

func TestDiagnosticOptionalCalibration(t *testing.T) {
	if diagnosticCalibration(nil) != nil {
		t.Fatal("missing diagnostics produced an attachment")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	n := newNetworkDiagnostics(networkDiagnosticsOptions{Client: &http.Client{}, Calibration: upstreamcheck.CalibrationOptions{
		Candidates: func(context.Context) upstreamcheck.CalibrationCandidates {
			close(entered)
			<-release
			return upstreamcheck.CalibrationCandidates{}
		},
	}})
	defer n.close()
	defer close(release)
	if diagnosticCalibration(n) != nil {
		t.Fatal("idle diagnostics produced an attachment")
	}
	if err := n.controlCalibration(false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("measurement did not enter preparation")
	}
	ready := make(chan []byte, 1)
	go func() { ready <- diagnosticCalibration(n) }()
	var data []byte
	select {
	case data = <-ready:
	case <-time.After(time.Second):
		t.Fatal("snapshot waited for measurement")
	}
	var snapshot struct {
		CollectedAt time.Time                       `json:"collectedAt"`
		Scope       string                          `json:"scope"`
		Status      upstreamcheck.CalibrationStatus `json:"status"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.Status.Running || snapshot.Status.Run != 1 || snapshot.CollectedAt.IsZero() || snapshot.Scope == "" {
		t.Fatal(snapshot)
	}
	dir := t.TempDir()
	for _, attachment := range [][]byte{nil, data} {
		var buffer bytes.Buffer
		sources, err := writeDiagnosticArchive(t.Context(), &buffer, diagnosticRange{From: 1, To: 2}, filepath.Join(dir, "missing.sqlite"), dir, dir, attachment)
		if err != nil {
			t.Fatal(err)
		}
		want := len(attachment) > 0
		if want && len(sources) != 4 {
			t.Fatal(sources)
		}
		if !want && len(sources) != 3 {
			t.Fatal("missing measurement added a source", sources)
		}
		z, err := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, f := range z.File {
			if f.Name == "network-calibration.json" {
				found = true
				r, err := f.Open()
				if err != nil {
					t.Fatal(err)
				}
				actual, err := io.ReadAll(r)
				r.Close()
				if err != nil || !bytes.Equal(actual, data) {
					t.Fatal("snapshot changed", err)
				}
			}
		}
		if found != want {
			t.Fatal("optional attachment", found, want)
		}
	}
}

func TestDiagnosticExportBrowser(t *testing.T) {
	evidence := os.Getenv("SWD_DIAGNOSTIC_BROWSER_EVIDENCE")
	if evidence == "" {
		t.Skip("set SWD_DIAGNOSTIC_BROWSER_EVIDENCE for real browser export")
	}
	c := testConsole(t)
	dir := t.TempDir()
	c.settings.VRChatLogDir = dir
	now := time.Now().Add(-time.Minute)
	if err := os.WriteFile(filepath.Join(dir, "output_log_fixture.txt"), []byte(now.Format("2006.01.02 15:04:05")+" Log - browser fixture\n  stack fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.files().LogDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.files().LogDir, now.Format("20060102")+".log"), []byte(now.Format(time.RFC3339Nano)+" [INFO] browser fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(c)
	c.address = server.Listener.Addr().String()
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "..", "webui", "test-support", "diagnostic-export-live.mjs"), server.URL, evidence)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
	for _, name := range []string{"blank.zip", "note.zip"} {
		z, err := zip.OpenReader(filepath.Join(evidence, name))
		if err != nil {
			t.Fatal(err)
		}
		foundNote, foundVRC, foundSWD := false, false, false
		for _, f := range z.File {
			r, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(r)
			r.Close()
			if err != nil {
				t.Fatal(err)
			}
			if f.Name == "用户说明.txt" {
				foundNote = true
				if string(data) != "播放中断，随后恢复" {
					t.Fatal(string(data))
				}
			}
			if strings.HasPrefix(f.Name, "vrc/") {
				foundVRC = strings.Contains(string(data), "stack fixture")
			}
			if strings.HasPrefix(f.Name, "swd/") {
				foundSWD = strings.Contains(string(data), "browser fixture")
			}
		}
		z.Close()
		if foundNote != (name == "note.zip") || !foundVRC || !foundSWD {
			t.Fatal(name, foundNote, foundVRC, foundSWD)
		}
	}
}

func TestDiagnosticArchiveBoundariesAndPartialSources(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 11, 14, 0, 0, 0, 0, time.Local)
	content := "2026.11.13 23:59:59 Log - old\nold stack\n2026.11.14 00:00:00 Error - selected\n  stack line\n2026.11.14 00:01:00 Log - excluded\nexcluded stack\n"
	if err := os.WriteFile(filepath.Join(dir, "output_log_fixture.txt"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20261114.log"), []byte(start.Format(time.RFC3339Nano)+" [INFO] selected\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	sources, err := writeDiagnosticArchive(t.Context(), &b, diagnosticRange{From: start.UnixMilli(), To: start.Add(time.Minute).UnixMilli(), Note: "用户观察"}, filepath.Join(dir, "missing.sqlite"), dir, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources[0].Issues) == 0 || sources[1].Records != 1 || sources[2].Records != 1 {
		t.Fatal(sources)
	}
	z, err := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range z.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name] = string(data)
	}
	if files["用户说明.txt"] != "用户观察" || files["vrc/output_log_fixture.txt"] != "2026.11.14 00:00:00 Error - selected\n  stack line\n" {
		t.Fatal(files)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := writeDiagnosticArchive(ctx, io.Discard, diagnosticRange{From: 1, To: 2}, "missing", dir, dir, nil); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestDiagnosticTimestampFormats(t *testing.T) {
	want := time.Date(2026, 11, 13, 16, 0, 0, 0, time.UTC)
	for _, line := range []string{`2026-11-14T00:00:00.0000+08:00 [INFO] event`, `{"time":"2026-11-13T16:00:00Z","msg":"old"}`} {
		got, ok := diagnosticTimestamp(line, false)
		if !ok || !got.Equal(want) {
			t.Fatal(line, got, ok)
		}
	}
	if _, ok := diagnosticTimestamp("unparseable", false); ok {
		t.Fatal("accepted missing timestamp")
	}
}

func TestDiagnosticExportAdmission(t *testing.T) {
	c := testConsole(t)
	diagnostics := c.networkDiagnostics
	for _, tc := range []struct {
		method, token string
		from, to      int64
		code          int
	}{{"POST", "", 1, 2, 403}, {"GET", c.token, 1, 2, 404}, {"POST", c.token, 2, 1, 400}, {"POST", c.token, 1, 2, 200}} {
		body, _ := json.Marshal(diagnosticRange{From: tc.from, To: tc.to})
		r := httptest.NewRequest(tc.method, "http://"+c.address+"/api/diagnostics/export", strings.NewReader(string(body)))
		r.Header.Set("X-SWD-Token", tc.token)
		w := httptest.NewRecorder()
		c.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatal(tc, w.Code, w.Body.String())
		}
		if w.Code == 200 {
			if _, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len())); err != nil {
				t.Fatal(err)
			}
		}
	}
	if c.service != nil || c.httpServer != nil {
		t.Fatal("export started services")
	}
	if c.networkDiagnostics != diagnostics {
		t.Fatal("export initialized diagnostics")
	}
}
