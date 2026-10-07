package console

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"still-wanna-dance/internal/legal"
)

type termsReceipt struct {
	Version    string    `json:"version"`
	Hash       string    `json:"sha256"`
	AcceptedAt time.Time `json:"acceptedAt"`
}

func (c *Console) termsAccepted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.terms.Version == legal.Version && c.terms.Hash == legal.Hash() && !c.terms.AcceptedAt.IsZero()
}

func (c *Console) loadTerms() error {
	b, err := os.ReadFile(c.configPath + ".terms.json")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// Malformed or old receipts never grant acceptance.
	var receipt termsReceipt
	if json.Unmarshal(b, &receipt) == nil {
		c.terms = receipt
	}
	return nil
}

func (c *Console) acceptTerms() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return fmt.Errorf("控制台正在退出")
	}
	receipt := termsReceipt{legal.Version, legal.Hash(), time.Now().UTC()}
	b, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(c.configPath), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(c.configPath), ".terms-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), c.configPath+".terms.json"); err != nil {
		return err
	}
	c.terms = receipt
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
		(c.terms.Version == legal.Version && c.terms.Hash == legal.Hash() && !c.terms.AcceptedAt.IsZero()) {
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
func (c *Console) serveTerms(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == "GET" && r.URL.Path == "/terms.txt" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, legal.Text)
		return true
	}
	if r.Method == "GET" && (r.URL.Path == "/terms" || (r.URL.Path == "/" && !c.termsAccepted())) {
		c.mu.Lock()
		c.cancelTermsExitLocked()
		c.termsPageID = rand.Text()
		pageID := c.termsPageID
		c.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = termsTemplate.Execute(w, struct {
			Text, Version, Hash, Token, PageID string
			Accepted                           bool
		}{legal.Text, legal.Version, legal.Hash(), c.token, pageID, c.termsAccepted()})
		return true
	}
	if r.URL.Path == "/api/terms/leave" || r.URL.Path == "/api/terms/return" {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return true
		}
		if r.Header.Get("X-StepStash-Token") != c.token || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+c.address) {
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
		if r.Header.Get("X-StepStash-Token") != c.token || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+c.address) {
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
			http.Error(w, "请重新阅读当前条款并主动勾选两项确认", 400)
			return true
		}
		if err := c.acceptTerms(); err != nil {
			http.Error(w, "无法保存同意记录，请检查配置目录权限后重试", 500)
			return true
		}
		writeJSON(w, map[string]bool{"ok": true})
		return true
	}
	if !c.termsAccepted() && r.URL.Path != "/api/identity" && r.URL.Path != "/api/stop" && r.URL.Path != "/api/queue/stop" && r.URL.Path != "/api/exit" && r.URL.Path != "/api/restart" && r.URL.Path != "/api/hosts/disable" && r.URL.Path != "/assets/console.css" && r.URL.Path != "/about" {
		http.Error(w, "请先打开控制台阅读并同意使用条款", http.StatusPreconditionRequired)
		return true
	}
	return false
}

var termsTemplate = template.Must(template.New("terms").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>使用条款 · Still Wanna Dance</title><link rel="stylesheet" href="/assets/console.css"></head>
<body><main style="max-width:880px;margin:32px auto;padding:24px"><h1>使用条款与内容权利声明</h1>
{{if .Accepted}}<p>本机已确认当前条款。<a href="/">返回控制台</a></p>{{end}}
<p><strong>MIT 仅授权本项目软件，不授予视频、音乐等第三方内容的使用权。能下载、已缓存或个人使用，均不当然等于获得授权。</strong></p>
{{if not .Accepted}}<p>请特别阅读第 2、4、5 条。不同意不会启用缓存或下载；仍可恢复 hosts。</p>
<p>未同意时，关闭或离开本页约 60 秒后程序将尝试自动退出；重新打开本页可取消。已同意后，关闭网页不会退出程序。</p>{{end}}
<p>条款版本：{{.Version}} · <a href="/terms.txt" target="_blank" rel="noopener">查看／保存纯文本</a></p>
<article style="white-space:pre-wrap;overflow-wrap:anywhere;line-height:1.8">{{.Text}}</article>
{{if not .Accepted}}
<form id="consent"><p><label><input type="checkbox" id="rights" required> 我理解软件许可不等于第三方内容授权，并将基于必要授权或其他合法依据使用内容。</label></p>
<p><label><input type="checkbox" id="agree" required> 我已阅读并同意完整条款，包括系统与数据影响以及担保与责任边界。</label></p>
<button type="submit" id="accept" disabled>同意并进入</button> <button type="button" id="decline">不同意，保持停用</button></form>{{end}}
<p><button type="button" id="restore">恢复 Still Wanna Dance 管理的 hosts</button> <button type="button" id="stop">停止缓存服务</button></p>
<p id="result" role="status" aria-live="polite"></p></main>
<script>
const token={{.Token}}, version={{.Version}}, sha256={{.Hash}}, pageID={{.PageID}};
const result=document.getElementById('result');
async function post(path,body){const r=await fetch(path,{method:'POST',headers:{'Content-Type':'application/json','X-StepStash-Token':token},body:JSON.stringify(body)});if(!r.ok)throw new Error(await r.text());}
const form=document.getElementById('consent');
if(form){const agree=document.getElementById('agree'), rights=document.getElementById('rights'), accept=document.getElementById('accept');let busy=false;
function presence(action){fetch('/api/terms/'+action,{method:'POST',keepalive:true,headers:{'Content-Type':'application/json','X-StepStash-Token':token},body:JSON.stringify({pageID})}).catch(()=>{});}
window.addEventListener('pagehide',()=>presence('leave'));
window.addEventListener('pageshow',e=>{if(e.persisted)presence('return');});
function update(){accept.disabled=busy||!agree.checked||!rights.checked;}form.addEventListener('change',update);
form.addEventListener('submit',async e=>{e.preventDefault();if(busy||!agree.checked||!rights.checked)return;busy=true;update();try{await post('/api/terms/accept',{version,sha256,agree:true,contentRights:true});location.replace('/');}catch(e){result.textContent=e.message;busy=false;update();}});
document.getElementById('decline').onclick=()=>{agree.checked=rights.checked=false;update();result.textContent='未同意，缓存与下载保持停用。可恢复 hosts 后关闭本页，程序将在约 60 秒后尝试自动退出；也可通过托盘退出。';};}
for(const [id,path] of [['restore','/api/hosts/disable'],['stop','/api/stop']]){document.getElementById(id).onclick=async()=>{try{await post(path,{});result.textContent=id==='restore'?'hosts 恢复操作已完成。':'缓存服务已停止。';}catch(e){result.textContent=e.message;}};}
</script></body></html>`))
