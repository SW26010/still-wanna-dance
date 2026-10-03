package console

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/cacheproxy"
)

type Song struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Checksum string `json:"-"`
}
type Failure struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Error string `json:"error"`
}
type Batch struct {
	BudgetReached bool      `json:"budgetReached"`
	CatalogHits   int       `json:"catalogHits"`
	FullVerify    bool      `json:"fullVerify"`
	Reused        int       `json:"reused"`
	Verified      int       `json:"verified"`
	Corrupt       int       `json:"corrupt"`
	ScanOnly      bool      `json:"scanOnly"`
	Missing       int       `json:"missing"`
	Updated       time.Time `json:"updated"`
	Finished      time.Time `json:"finished"`
	Running       bool      `json:"running"`
	Phase         string    `json:"phase"`
	Total         int       `json:"total"`
	Checked       int       `json:"checked"`
	Hits          int       `json:"hits"`
	Downloaded    int       `json:"downloaded"`
	Failed        int       `json:"failed"`
	Current       string    `json:"current"`
	Failures      []Failure `json:"failures"`
}

func parseCatalog(r io.Reader) ([]Song, error) {
	var catalog struct {
		Groups struct {
			Contents []struct {
				SongInfos []Song `json:"songInfos"`
			} `json:"contents"`
		} `json:"groups"`
	}
	d := json.NewDecoder(io.LimitReader(r, 16<<20))
	if err := d.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("歌曲列表格式错误：%w", err)
	}
	seen := map[int64]bool{}
	songs := []Song{}
	for _, g := range catalog.Groups.Contents {
		for _, s := range g.SongInfos {
			if s.ID > 0 && !seen[s.ID] {
				seen[s.ID] = true
				songs = append(songs, s)
			}
		}
	}
	if len(songs) == 0 {
		return nil, errors.New("歌曲列表为空或格式不受支持；未开始处理曲目")
	}
	sort.Slice(songs, func(i, j int) bool { return songs[i].ID < songs[j].ID })
	return songs, nil
}

func (c *Console) catalog(ctx context.Context) ([]Song, error) {
	return c.catalogForScan(ctx, false)
}

func (c *Console) catalogForScan(ctx context.Context, scan bool) ([]Song, error) {
	base := c.apiBase
	if base == "http://api.udon.dance" {
		base = "https://api.udon.dance"
	}
	r, err := http.NewRequestWithContext(ctx, "GET", base+"/Api/Songs/list", nil)
	if err != nil {
		return nil, err
	}
	// The full catalog is much larger than a playback redirect. Give its body
	// its own budget without changing the shared client's per-song timeout.
	client := *c.upstreamClient()
	client.Timeout = 2 * time.Minute
	resp, err := client.Do(r)
	if err != nil {
		return nil, applog.SafeError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("歌曲列表接口返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("歌曲列表读取失败（网络中断或超时），请重试：%w", applog.SafeError(err))
	}
	if len(body) > 16<<20 {
		return nil, errors.New("歌曲列表超过 16 MiB 大小限制")
	}
	songs, err := parseCatalog(bytes.NewReader(body))
	if err == nil && scan && c.checksumURL != "" {
		var stamp struct {
			Time string `json:"time"`
		}
		if json.Unmarshal(body, &stamp) == nil && stamp.Time != "" {
			c.addCatalogChecksums(ctx, songs, stamp.Time)
		}
	}
	return songs, err
}

func (c *Console) resolve(ctx context.Context, id int64) (string, error) {
	return c.resolveNode(ctx, id, "hkg")
}

func (c *Console) resolveNode(ctx context.Context, id int64, upstream string) (string, error) {
	node, host := "nya", "nya.xin.moe"
	if upstream == "cf" {
		node, host = "cf", "play.udon.dance"
	}
	r, err := http.NewRequestWithContext(ctx, "GET", c.apiBase+"/Api/Songs/play?node="+node+"&id="+strconv.FormatInt(id, 10), nil)
	if err != nil {
		return "", applog.SafeError(err)
	}
	resp, err := c.upstreamClient().Do(r)
	if err != nil {
		return "", applog.SafeError(err)
	}
	defer resp.Body.Close()
	if !cacheproxy.IsSongRedirect(resp.StatusCode) {
		return "", fmt.Errorf("歌曲地址接口返回 %d", resp.StatusCode)
	}
	u, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		return "", applog.SafeError(err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || (u.Host != "play.udon.dance" && u.Host != "nya.xin.moe") || u.User != nil || u.Fragment != "" {
		return "", errors.New("歌曲返回了未支持的视频地址；当前支持 CF/HKG HTTP 和 HTTPS")
	}
	if u.Host != host {
		return "", fmt.Errorf("%s 返回了其他上游的视频地址", upstream)
	}
	if err := cacheproxy.ValidateVideoURL(u.String(), cacheproxy.DefaultConfig().MaxFileBytes); err != nil {
		return "", fmt.Errorf("歌曲返回了无效的视频地址：%w", err)
	}
	return u.String(), nil
}

func (c *Console) startBatch() error { return c.startBatchMode(false) }

func (c *Console) startBatchMode(scanOnly bool) error {
	return c.startBatchCheck(scanOnly, false)
}

func (c *Console) startBatchCheck(scanOnly, fullVerify bool) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return c.startBatchCheckLocked(scanOnly, fullVerify)
}

func (c *Console) startBatchCheckLocked(scanOnly, fullVerify bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return errors.New("控制台正在退出")
	}
	if c.batch.Running {
		return errors.New("已有批量任务正在运行")
	}
	if c.queue.Running && !scanOnly {
		return errors.New("请先停止队列预缓存")
	}
	if scanOnly {
		c.mu.Unlock()
		_, err := os.ReadDir(c.settings.StorageDir)
		c.mu.Lock()
		if err != nil {
			return fmt.Errorf("无法扫描目录 %s：%w", c.settings.StorageDir, err)
		}
	}
	if !scanOnly {
		if err := c.ensureEngine(); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.batchCancel = cancel
	c.batchDone = make(chan struct{})
	c.batch = Batch{Running: true, ScanOnly: scanOnly, FullVerify: fullVerify, Phase: "正在获取最新歌曲列表"}
	if !scanOnly && c.scanPlan != nil && c.scanPlan.settings == c.settings {
		c.batch.Phase = "正在复用扫描结果下载补齐"
	}
	slog.Info("batch_started", "scan_only", scanOnly)
	go c.runBatch(ctx, c.service, c.batchDone)
	return nil
}

func (c *Console) runBatch(ctx context.Context, s *cacheproxy.Server, done chan struct{}) {
	defer close(done)
	started := time.Now()
	completed := false
	c.mu.Lock()
	scanOnly, settings := c.batch.ScanOnly, c.settings
	fullVerify := c.batch.FullVerify
	plan := c.scanPlan
	c.scanPlan = nil
	if scanOnly || (plan != nil && plan.settings != settings) {
		plan = nil
	}
	c.mu.Unlock()
	if !scanOnly {
		ctx = s.WithBatchBudget(ctx)
	}
	budgetStop := make(chan struct{})
	var stopBudget sync.Once
	defer func() {
		c.lifecycleMu.Lock()
		defer c.lifecycleMu.Unlock()
		c.mu.Lock()
		result := c.batch
		result.Failures = append([]Failure(nil), c.batch.Failures...)
		c.mu.Unlock()
		result.Running = false
		result.Finished = time.Now()
		if ctx.Err() != nil {
			result.Phase = "任务已停止，上次成功结果保留。已开始的共享下载可能继续完成。"
		}
		if completed && ctx.Err() == nil && result.Total > 0 && result.Checked == result.Total && result.Failed == 0 {
			result.Updated = time.Now()
			if err := c.writeSnapshot("batch", settings, result); err != nil {
				result.Phase += "；保存失败，上次成功结果保留：" + err.Error()
			} else {
				c.mu.Lock()
				c.lastBatch = result
				c.mu.Unlock()
			}
		}
		if err := c.writeSnapshot("attempt", settings, result); err != nil {
			result.Phase += "；无法保存本次进度：" + err.Error()
		}
		c.mu.Lock()
		c.batch = result
		c.batchCancel = nil
		c.mu.Unlock()
		c.resumeQueueLocked()
		slog.Info("batch_finished", "scan_only", scanOnly, "full_verify", fullVerify, "reused", result.Reused, "verified", result.Verified, "corrupt", result.Corrupt, "completed", completed, "elapsed_ms", time.Since(started).Milliseconds(), "missing", result.Missing, "cancelled", ctx.Err() != nil, "total", result.Total, "checked", result.Checked, "hits", result.Hits, "downloaded", result.Downloaded, "failed", result.Failed, "phase", result.Phase)
	}()
	var checker *cacheproxy.LocalChecker
	if scanOnly {
		var err error
		checker, err = cacheproxy.NewLocalChecker(settings.StorageDir, fullVerify)
		if err != nil {
			c.mu.Lock()
			c.batch.Phase = "无法打开校验记录：" + err.Error()
			c.mu.Unlock()
			return
		}
		defer checker.Close()
	}
	var songs []Song
	var err error
	if plan != nil {
		songs = plan.songs
	} else {
		songs, err = c.catalogForScan(ctx, scanOnly)
	}
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("catalog_failed", "error", err)
		}
		c.mu.Lock()
		c.batch.Phase = "获取歌曲列表失败：" + err.Error()
		c.mu.Unlock()
		return
	}
	var localTargets map[string]cacheproxy.ScanTarget
	if !scanOnly {
		ids := make([]int64, len(songs))
		for i, song := range songs {
			ids[i] = song.ID
		}
		priorities, err := s.EffectiveSongPriorities(ctx, ids)
		if err != nil {
			c.mu.Lock()
			c.batch.Phase = "读取下载优先级失败：" + err.Error()
			c.mu.Unlock()
			return
		}
		songs = append([]Song(nil), songs...)
		sort.Slice(songs, func(i, j int) bool {
			if priorities[songs[i].ID] != priorities[songs[j].ID] {
				return priorities[songs[i].ID] > priorities[songs[j].ID]
			}
			return songs[i].ID > songs[j].ID
		})
	}
	if scanOnly {
		localTargets, err = cacheproxy.LoadScanTargets(ctx, settings.StorageDir)
		if err != nil {
			slog.Warn("scan_local_index_unavailable", "error", err)
		}
	}
	c.mu.Lock()
	c.batch.Total = len(songs)
	slog.Info("catalog_loaded", "total", len(songs))
	c.batch.Phase = "正在下载补齐"
	if scanOnly {
		c.batch.Phase = "正在增量扫描（不下载）"
		if fullVerify {
			c.batch.Phase = "正在完整校验（读取全部视频，不下载）"
		}
	}
	c.mu.Unlock()
	active := map[int64]string{}
	results := make(map[int64]scanResult)
	var scanner *scanLimiter
	if scanOnly {
		scanner = newScanLimiter(settings)
		defer scanner.ticker.Stop()
	}
	refreshCurrent := func() {
		names := make([]string, 0, len(active))
		for _, name := range active {
			names = append(names, name)
		}
		sort.Strings(names)
		c.batch.Current = strings.Join(names, "；")
	}
	process := func(song Song) {
		select {
		case <-budgetStop:
			return
		default:
		}
		if ctx.Err() != nil {
			return
		}
		ctx, releaseBudget := cacheproxy.WithBatchSongBudget(ctx)
		defer releaseBudget()
		c.mu.Lock()
		active[song.ID] = fmt.Sprintf("%d · %s", song.ID, song.Name)
		refreshCurrent()
		c.mu.Unlock()
		defer func() {
			c.mu.Lock()
			delete(active, song.ID)
			refreshCurrent()
			c.mu.Unlock()
		}()
		var err error
		var reused bool
		source := ""
		var result scanResult
		var localResult cacheproxy.LocalCheckResult
		if scanOnly {
			var hit bool
			if known, ok := localTargets[strconv.FormatInt(song.ID, 10)]; ok && song.Checksum != "" && known.Checksum == song.Checksum {
				err = scanAcquire(ctx, scanner.local)
				if err == nil {
					localResult, err = checker.Check(ctx, known.Target)
					<-scanner.local
					if localResult.Hit {
						hit, result.target, result.receipt = true, known.Target, localResult.Receipt
						result.localOnly = true
					}
				}
			}
			if !hit && err == nil {
				hit, err = scanner.check(ctx, func() (string, error) {
					return c.resolve(ctx, song.ID)
				}, func(target string) (bool, error) {
					result.target = target
					var hit bool
					localResult, err = checker.Check(ctx, target)
					hit, result.receipt = localResult.Hit, localResult.Receipt
					return hit, err
				})
			}
			if hit {
				source = "HIT"
			} else {
				source = "MISSING"
			}
		} else {
			err = s.SetSongTitle(ctx, strconv.FormatInt(song.ID, 10), song.Name)
			if err == nil {
				if plan != nil && settings.DownloadUpstream != "cf" {
					result = plan.results[song.ID]
				}
				reused, err = s.ReuseLocalSong(ctx, strconv.FormatInt(song.ID, 10), result.target, result.receipt)
				switch {
				case err != nil:
					// Preserve association errors in the batch failure result.
				case reused:
					source = "HIT"
				case !result.localOnly && result.target != "" && settings.DownloadUpstream == "hkg":
					source, err = s.PrefetchSong(ctx, strconv.FormatInt(song.ID, 10), result.target)
					if err != nil && ctx.Err() == nil && !errors.Is(err, cacheproxy.ErrBatchBudget) {
						previous := err
						source, err = c.prefetchSong(ctx, s, song.ID, nil)
						err = preserveBudgetFailure(previous, err)
					}
				default:
					// Scan URLs use HKG. Reuse verified local hits above, but
					// resolve CF first for Auto downloads of missing files.
					source, err = c.prefetchSong(ctx, s, song.ID, nil)
				}
			}
		}
		if errors.Is(err, cacheproxy.ErrBatchBudget) {
			stopBudget.Do(func() { close(budgetStop) })
			var failed *batchFallbackBudgetError
			if !errors.As(err, &failed) {
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		if scanOnly && err == nil {
			if result.localOnly {
				c.batch.CatalogHits++
			}
			results[song.ID] = result
			if localResult.Reused {
				c.batch.Reused++
			} else if localResult.Hit {
				c.batch.Verified++
			}
			if localResult.Corrupt {
				c.batch.Corrupt++
			}
		}
		c.batch.Checked++
		if err != nil {
			c.batch.Failed++
			slog.Warn("batch_song_failed", "song_id", song.ID, "error", err)
			c.batch.Failures = append(c.batch.Failures, Failure{song.ID, song.Name, err.Error()})
		} else if source == "MISSING" {
			c.batch.Missing++
		} else if source == "HIT" {
			c.batch.Hits++
		} else {
			c.batch.Downloaded++
		}
		if c.batch.Checked%100 == 0 || c.batch.Checked == c.batch.Total {
			slog.Info("batch_progress", "scan_only", scanOnly, "total", c.batch.Total, "checked", c.batch.Checked,
				"hits", c.batch.Hits, "missing", c.batch.Missing, "downloaded", c.batch.Downloaded, "failed", c.batch.Failed)
		}
		c.mu.Unlock()
		if scanOnly || reused {
			return // Scan request starts are paced globally by scanLimiter.
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
	}
	workers := 2
	if scanOnly {
		workers = settings.ScanResolveConcurrency + settings.ScanCheckConcurrency
	}
	jobs := make(chan Song)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for song := range jobs {
				process(song)
			}
		}()
	}
dispatch:
	for _, song := range songs {
		select {
		case <-budgetStop:
			break dispatch
		case <-ctx.Done():
			break dispatch
		case jobs <- song:
		}
	}
	close(jobs)
	wg.Wait()
	select {
	case <-budgetStop:
		c.mu.Lock()
		c.batch.BudgetReached = true
		c.batch.Phase = "容量预算不足，下载补齐已自动停止。请前往「设置」调大「视频缓存上限（GiB）」后再次下载补齐；修改前请先停止 CDN 和队列预缓存。"
		c.mu.Unlock()
		return
	default:
	}
	if ctx.Err() != nil {
		return
	}
	if scanOnly {
		if _, err := os.ReadDir(settings.StorageDir); err != nil {
			c.mu.Lock()
			c.batch.Phase = "扫描目录已不可用，上次结果保留：" + err.Error()
			c.mu.Unlock()
			return
		}
	}
	completed = true
	c.mu.Lock()
	if scanOnly {
		c.scanPlan = &scanPlan{settings: settings, songs: songs, results: results}
	}
	c.batch.Current = ""
	if c.batch.Failed > 0 {
		c.batch.Phase = "检查完成，部分歌曲失败；再次检查可重试"
	} else if scanOnly {
		c.batch.Phase = "扫描完成；缺失或损坏的视频可通过「下载补齐」更新"
	} else {
		c.batch.Phase = "所有已知歌曲已处理完成；计数为本次检查和下载结果，视频可能因容量限制被淘汰，当前保留情况请扫描本地文件"
	}
	c.mu.Unlock()
}
