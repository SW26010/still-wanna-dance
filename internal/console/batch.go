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
	Reused        int       `json:"reused"`
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

type songCatalog struct {
	Songs            []Song
	MD5              *cacheproxy.Catalog
	Revision, Source string
}

func parseCatalog(r io.Reader) (songCatalog, error) {
	var catalog struct {
		Time   string `json:"time"`
		Groups struct {
			Contents []struct {
				SongInfos []Song `json:"songInfos"`
			} `json:"contents"`
		} `json:"groups"`
	}
	d := json.NewDecoder(io.LimitReader(r, 16<<20))
	if err := d.Decode(&catalog); err != nil {
		return songCatalog{}, fmt.Errorf("歌曲列表格式错误：%w", err)
	}
	if _, err := cacheproxy.ParseCatalogTime(catalog.Time); err != nil {
		return songCatalog{}, err
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
		return songCatalog{}, errors.New("歌曲列表为空或格式不受支持；未开始处理曲目")
	}
	sort.Slice(songs, func(i, j int) bool { return songs[i].ID < songs[j].ID })
	return songCatalog{Songs: songs, Revision: catalog.Time}, nil
}

func (c *Console) catalog(ctx context.Context) (songCatalog, error) {
	if c.checksumURL != "" {
		snapshot, err := c.fetchCatalogSnapshot(ctx)
		if err == nil {
			songs := make([]Song, 0, len(snapshot.Songs))
			for _, song := range snapshot.Songs {
				name := ""
				if song.Name != nil {
					name = *song.Name
				}
				songs = append(songs, Song{ID: song.ID, Name: name, Checksum: song.MD5})
			}
			sort.Slice(songs, func(i, j int) bool { return songs[i].ID < songs[j].ID })
			return songCatalog{Songs: songs, MD5: &snapshot, Revision: snapshot.Revision, Source: snapshot.Source}, nil
		}
		if errors.Is(err, cacheproxy.ErrInvalidCatalogMapping) {
			return songCatalog{}, err
		}
		if ctx.Err() != nil {
			return songCatalog{}, ctx.Err()
		}
		slog.Warn("catalog_md5_unavailable", "error", err)
	}

	base := c.apiBase
	if base == "http://api.udon.dance" {
		base = "https://api.udon.dance"
	}
	r, err := http.NewRequestWithContext(ctx, "GET", base+"/Api/Songs/list", nil)
	if err != nil {
		return songCatalog{}, err
	}
	// The full catalog is much larger than a playback redirect. Give its body
	// its own budget without changing the shared client's per-song timeout.
	client := *c.upstreamClient()
	client.Timeout = 2 * time.Minute
	resp, err := client.Do(r)
	if err != nil {
		return songCatalog{}, applog.SafeError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return songCatalog{}, fmt.Errorf("歌曲列表接口返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		if ctx.Err() != nil {
			return songCatalog{}, ctx.Err()
		}
		return songCatalog{}, fmt.Errorf("歌曲列表读取失败（网络中断或超时），请重试：%w", applog.SafeError(err))
	}
	if len(body) > 16<<20 {
		return songCatalog{}, errors.New("歌曲列表超过 16 MiB 大小限制")
	}
	catalog, err := parseCatalog(bytes.NewReader(body))
	catalog.Source = r.URL.String()
	return catalog, err
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
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return c.startBatchModeLocked(scanOnly)
}

func (c *Console) startBatchModeLocked(scanOnly bool) error {
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
	if err := c.ensureEngine(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.batchCancel = cancel
	c.batchDone = make(chan struct{})
	c.batch = Batch{Running: true, ScanOnly: scanOnly, Phase: "正在获取最新歌曲列表"}
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
		slog.Info("batch_finished", "scan_only", scanOnly, "reused", result.Reused, "completed", completed, "elapsed_ms", time.Since(started).Milliseconds(), "missing", result.Missing, "cancelled", ctx.Err() != nil, "total", result.Total, "checked", result.Checked, "hits", result.Hits, "downloaded", result.Downloaded, "failed", result.Failed, "phase", result.Phase)
	}()

	catalog, err := c.catalog(ctx)
	fail := func(message string) { c.mu.Lock(); c.batch.Phase = message; c.mu.Unlock() }
	if err != nil {
		fail("获取歌曲列表失败：" + err.Error())
		return
	}
	songs := catalog.Songs
	targets := map[string]string{}
	titles := map[string]string{}
	for _, song := range songs {
		titles[strconv.FormatInt(song.ID, 10)] = song.Name
	}
	if catalog.MD5 != nil {
		if err := s.SyncCatalog(ctx, *catalog.MD5); err != nil {
			fail("更新歌曲映射失败：" + err.Error())
			return
		}
	} else {
		if err := s.SyncCatalogNames(ctx, catalog.Revision, catalog.Source, titles); err != nil {
			fail(err.Error())
			return
		}
	}
	// Only the fallback ID-only catalog needs per-song resolution before presence checks.
	var unresolved []Failure
	unresolvedIDs := make(map[string]bool)
	for _, song := range songs {
		if song.Checksum != "" {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		route := settings.DownloadUpstream
		if route == "auto" || route == "" {
			route = "cf"
		}
		if scanOnly {
			route = "hkg"
		}
		target, err := c.resolveNode(ctx, song.ID, route)
		if err != nil && settings.DownloadUpstream == "auto" && !scanOnly {
			target, err = c.resolveNode(ctx, song.ID, "hkg")
		}
		if err == nil {
			_, err = s.RememberSongURL(ctx, strconv.FormatInt(song.ID, 10), target)
		}
		if err != nil {
			id := strconv.FormatInt(song.ID, 10)
			if !unresolvedIDs[id] {
				unresolved = append(unresolved, Failure{ID: song.ID, Name: song.Name, Error: err.Error()})
				unresolvedIDs[id] = true
			}
			continue
		}
		targets[strconv.FormatInt(song.ID, 10)] = target
	}
	check, release, err := s.CheckReferences(ctx)
	if err != nil {
		fail("检查本地资源失败：" + err.Error())
		return
	}
	defer release()
	type resourceJob struct {
		md5   string
		ids   []string
		score float64
	}
	grouped := map[string]*resourceJob{}
	total := len(unresolved)
	for id, key := range check.References {
		// A persisted mapping survives resolution failure, but that song must
		// have only one outcome in this batch: failure, not also hit/missing.
		if unresolvedIDs[id] {
			continue
		}
		total++
		if grouped[key] == nil {
			grouped[key] = &resourceJob{md5: key}
		}
		grouped[key].ids = append(grouped[key].ids, id)
	}
	priorities, err := s.EffectiveResourcePriorities(ctx, check.References)
	if err != nil {
		fail(err.Error())
		return
	}
	// Retained mappings still contribute to shared-resource priority, even
	// when resolution failed for that ID in this batch.
	for key, job := range grouped {
		job.score = priorities[key].Score
	}
	jobs := make([]*resourceJob, 0, len(grouped))
	for _, job := range grouped {
		sort.Slice(job.ids, func(i, j int) bool {
			a, _ := strconv.ParseInt(job.ids[i], 10, 64)
			b, _ := strconv.ParseInt(job.ids[j], 10, 64)
			return a > b
		})
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].score != jobs[j].score {
			return jobs[i].score > jobs[j].score
		}
		a, _ := strconv.ParseInt(jobs[i].ids[0], 10, 64)
		b, _ := strconv.ParseInt(jobs[j].ids[0], 10, 64)
		return a > b
	})
	c.mu.Lock()
	c.batch.Total = total
	c.batch.Checked = len(unresolved)
	c.batch.Failed = len(unresolved)
	c.batch.Failures = unresolved
	if scanOnly {
		c.batch.Phase = "正在检查 MD5 文件是否齐备"
	} else {
		c.batch.Phase = "正在按缺失 MD5 下载补齐"
	}
	c.mu.Unlock()
	process := func(job *resourceJob) {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-budgetStop:
			return
		default:
		}
		hit := check.Present[job.md5]
		var err error
		source := "HIT"
		if !hit && !scanOnly {
			songCtx, finish := cacheproxy.WithBatchSongBudget(ctx)
			defer finish()
			songCtx = cacheproxy.WithExpectedMD5(songCtx, job.md5)
			// Keep resource scheduling independent of which ID resolves it.
			// Prefer IDs still in the catalog, then try retained associations.
			candidates := append([]string(nil), job.ids...)
			sort.SliceStable(candidates, func(i, j int) bool {
				_, currentI := titles[candidates[i]]
				_, currentJ := titles[candidates[j]]
				return currentI && !currentJ
			})
			for _, id := range candidates {
				n, _ := strconv.ParseInt(id, 10, 64)
				c.mu.Lock()
				c.batch.Current = id + " · " + titles[id]
				c.mu.Unlock()
				if target := targets[id]; target != "" {
					source, err = s.PrefetchSong(songCtx, id, target)
					if err != nil && songCtx.Err() == nil && !errors.Is(err, cacheproxy.ErrBatchBudget) {
						previous := err
						source, err = c.prefetchSong(songCtx, s, n, nil)
						err = preserveBudgetFailure(previous, err)
					}
				} else {
					source, err = c.prefetchSong(songCtx, s, n, nil)
				}
				if err == nil || songCtx.Err() != nil || errors.Is(err, cacheproxy.ErrBatchBudget) {
					break
				}
			}
			if errors.Is(err, cacheproxy.ErrBatchBudget) {
				stopBudget.Do(func() { close(budgetStop) })
				var failed *batchFallbackBudgetError
				if !errors.As(err, &failed) {
					return
				}
			}
		}
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		c.batch.Checked += len(job.ids)
		switch {
		case err != nil:
			c.batch.Failed += len(job.ids)
			for _, id := range job.ids {
				n, _ := strconv.ParseInt(id, 10, 64)
				c.batch.Failures = append(c.batch.Failures, Failure{ID: n, Name: titles[id], Error: err.Error()})
			}
		case hit:
			c.batch.Hits += len(job.ids)
			c.batch.Reused += len(job.ids)
			c.batch.CatalogHits += len(job.ids)
		case scanOnly:
			c.batch.Missing += len(job.ids)
		case source == "HIT":
			c.batch.Hits += len(job.ids)
		default:
			c.batch.Downloaded += len(job.ids)
		}
	}
	work := make(chan *resourceJob)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range work {
				process(job)
			}
		}()
	}
dispatch:
	for _, job := range jobs {
		select {
		case <-ctx.Done():
			break dispatch
		case <-budgetStop:
			break dispatch
		case work <- job:
		}
	}
	close(work)
	wg.Wait()
	select {
	case <-budgetStop:
		c.mu.Lock()
		c.batch.BudgetReached = true
		c.batch.Phase = "容量预算不足，下载补齐已自动停止。请前往「设置」调大「视频缓存上限（GiB）」后再次下载补齐。"
		c.mu.Unlock()
		return
	default:
	}
	if ctx.Err() != nil {
		return
	}
	completed = true
	c.mu.Lock()
	defer c.mu.Unlock()
	c.batch.Current = ""
	if c.batch.Failed > 0 {
		c.batch.Phase = "检查完成，部分歌曲失败；再次检查可重试"
	} else if scanOnly {
		c.batch.Phase = "MD5 文件检查完成；可通过下载补齐获取缺失资源"
	} else {
		c.batch.Phase = "所有已知歌曲已处理完成；同 MD5 共用一份视频"
	}
}
