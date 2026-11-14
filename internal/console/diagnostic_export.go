package console

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/buildinfo"
	"still-wanna-dance/internal/cacheproxy"
)

type diagnosticRange struct {
	From int64  `json:"from"`
	To   int64  `json:"to"`
	Note string `json:"note"`
}
type diagnosticSource struct {
	Name    string   `json:"name"`
	Records int64    `json:"records"`
	Files   int      `json:"files"`
	Issues  []string `json:"issues,omitempty"`
}

func (c *Console) serveDiagnosticExport(w http.ResponseWriter, r *http.Request) {
	var input diagnosticRange
	if err := decodeRequest(json.NewDecoder(r.Body), &input); err != nil || input.From <= 0 || input.To <= input.From || input.To > time.Now().Add(time.Minute).UnixMilli() || len(input.Note) > 6000 {
		http.Error(w, "请选择有效的起止时间；问题说明最多 2000 字。", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	c.mu.Lock()
	database, vrcDir := c.settings.DatabasePath, c.settings.effectiveVRChatLogDir()
	logDir := c.files().LogDir
	c.mu.Unlock()
	file, err := os.CreateTemp("", "swd-diagnostics-*.zip")
	if err != nil {
		preserveResponseFault(w, err)
		http.Error(w, "无法创建诊断包", 500)
		return
	}
	defer os.Remove(file.Name())
	defer file.Close()
	sources, err := writeDiagnosticArchive(ctx, file, input, database, logDir, vrcDir)
	if err != nil {
		preserveResponseFault(w, err)
		http.Error(w, "诊断包生成失败，请重试或缩短时间范围", 500)
		return
	}
	info, err := file.Stat()
	if err != nil {
		http.Error(w, "无法读取诊断包", 500)
		return
	}
	partial := false
	for _, s := range sources {
		partial = partial || len(s.Issues) > 0
	}
	w.Header().Set("X-SWD-Diagnostics-Partial", fmt.Sprint(partial))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="swd-diagnostics.zip"`)
	http.ServeContent(w, r, "swd-diagnostics.zip", info.ModTime(), file)
}

func writeDiagnosticArchive(ctx context.Context, w io.Writer, input diagnosticRange, database, logDir, vrcDir string) ([]diagnosticSource, error) {
	z := zip.NewWriter(w)
	write := func(name string, data []byte) error {
		entry, err := z.Create(name)
		if err != nil {
			return err
		}
		_, err = entry.Write(data)
		return err
	}
	source := diagnosticSource{Name: "requests"}
	entry, err := z.Create("requests.jsonl")
	if err != nil {
		return nil, err
	}
	source.Records, err = cacheproxy.ExportRequestHistory(ctx, database, input.From, input.To, entry)
	if err != nil {
		source.Issues = append(source.Issues, err.Error())
	}
	source.Files = 1
	sources := []diagnosticSource{source}
	for _, kind := range []struct{ name, dir string }{{"swd", logDir}, {"vrc", vrcDir}} {
		result, err := collectDiagnosticLogs(ctx, z, kind.name, kind.dir, input)
		if err != nil {
			return nil, err
		}
		sources = append(sources, result)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.Note) != "" {
		if err := write("用户说明.txt", []byte(input.Note)); err != nil {
			return nil, err
		}
	}
	manifest := struct {
		Build       buildinfo.Info     `json:"build"`
		Range       diagnosticRange    `json:"range"`
		CollectedAt time.Time          `json:"collectedAt"`
		VRCTimeZone string             `json:"vrcTimeZone"`
		Sources     []diagnosticSource `json:"sources"`
	}{buildinfo.Current(), diagnosticRange{From: input.From, To: input.To}, time.Now(), time.Local.String(), sources}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err = write("manifest.json", data); err != nil {
		return nil, err
	}
	explanation := fmt.Sprintf("时间范围：[ %s, %s )，包含开始、不包含结束。\n数据库按请求到达时间筛选，保留读取快照时的字段，null 完成时间表示尚无结束记录；时间戳为 Unix 毫秒。\nSWD 按记录内的时区解析；VRC 无时区时间按本机时区 %s 解释，无法还原历史时区变更。\n各来源分别读取，不构成跨来源的原子快照；文件只读取打开时的大小，不等待后续日志。\n无匹配记录不证明期间没有活动，日志级别、保留清理和读取失败都可能影响结果；详见 manifest.json。\n资料未自动脱敏，可能包含本机路径、玩家、房间和链接信息，分享前请检查。\n", time.UnixMilli(input.From).Format(time.RFC3339), time.UnixMilli(input.To).Format(time.RFC3339), time.Local)
	if err = write("说明.txt", []byte(explanation)); err != nil {
		return nil, err
	}
	return sources, z.Close()
}

func diagnosticTimestamp(line string, vrc bool) (time.Time, bool) {
	if vrc {
		if len(line) < 19 {
			return time.Time{}, false
		}
		t, err := time.ParseInLocation("2006.01.02 15:04:05", line[:19], time.Local)
		return t, err == nil
	}
	// Retained releases used JSON lines before the structured text format.
	if strings.HasPrefix(line, "{") {
		var record struct {
			Time time.Time `json:"time"`
		}
		err := json.Unmarshal([]byte(line), &record)
		return record.Time, err == nil && !record.Time.IsZero()
	}
	stamp, _, _ := strings.Cut(line, " ")
	t, err := time.Parse(time.RFC3339Nano, stamp)
	return t, err == nil
}

func collectDiagnosticLogs(ctx context.Context, z *zip.Writer, kind, dir string, input diagnosticRange) (diagnosticSource, error) {
	result := diagnosticSource{Name: kind}
	root, err := os.OpenRoot(dir)
	if err != nil {
		result.Issues = append(result.Issues, err.Error())
		return result, nil
	}
	defer root.Close()
	folder, err := root.Open(".")
	if err != nil {
		result.Issues = append(result.Issues, err.Error())
		return result, nil
	}
	defer folder.Close()
	entries, err := folder.ReadDir(-1)
	if err != nil {
		result.Issues = append(result.Issues, err.Error())
		return result, nil
	}
	candidates := 0
	for _, e := range entries {
		name := e.Name()
		vrc := kind == "vrc"
		if (!vrc && !applog.IsLogFile(name)) || (vrc && (!strings.HasPrefix(name, "output_log_") || !strings.HasSuffix(name, ".txt"))) {
			continue
		}
		candidates++
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !e.Type().IsRegular() {
			result.Issues = append(result.Issues, name+": 非普通文件，已跳过")
			continue
		}
		f, err := root.Open(name)
		if err != nil {
			result.Issues = append(result.Issues, err.Error())
			continue
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			result.Issues = append(result.Issues, err.Error())
			continue
		}
		scanner := bufio.NewScanner(io.LimitReader(f, info.Size()))
		scanner.Buffer(make([]byte, 64*1024), 4<<20)
		selected, known := false, false
		var output io.Writer
		unparsed := 0
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				f.Close()
				return result, err
			}
			line := scanner.Text()
			stamp, ok := diagnosticTimestamp(line, vrc)
			if ok {
				known = true
				selected = stamp.UnixMilli() >= input.From && stamp.UnixMilli() < input.To
			} else if !vrc || !known {
				unparsed++
				selected = false
			}
			if !selected {
				continue
			}
			if output == nil {
				output, err = z.Create(kind + "/" + name)
				if err != nil {
					f.Close()
					return result, err
				}
				result.Files++
			}
			if _, err = fmt.Fprintln(output, line); err != nil {
				f.Close()
				return result, err
			}
			if ok {
				result.Records++
			}
		}
		readErr := scanner.Err()
		f.Close()
		if readErr != nil {
			result.Issues = append(result.Issues, name+": "+readErr.Error())
		}
		if unparsed > 0 {
			result.Issues = append(result.Issues, fmt.Sprintf("%s: %d 行无法确定时间，已跳过", name, unparsed))
		}
	}
	if candidates == 0 {
		result.Issues = append(result.Issues, "未找到日志文件")
	}
	return result, nil
}
