package console

import (
	"context"
	"errors"
	"fmt"
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
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
	"still-wanna-dance/internal/videometa"
)

type Failure struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Error string `json:"error"`
}
type Batch struct {
	Catalog        cacheproxy.CatalogStatus `json:"catalog"`
	CatalogWarning string                   `json:"catalogWarning"`
	BudgetReached  bool                     `json:"budgetReached"`
	CatalogHits    int                      `json:"catalogHits"`
	Reused         int                      `json:"reused"`
	ScanOnly       bool                     `json:"scanOnly"`
	Missing        int                      `json:"missing"`
	Updated        time.Time                `json:"updated"`
	Finished       time.Time                `json:"finished"`
	Running        bool                     `json:"running"`
	Phase          string                   `json:"phase"`
	Total          int                      `json:"total"`
	Checked        int                      `json:"checked"`
	Hits           int                      `json:"hits"`
	Downloaded     int                      `json:"downloaded"`
	Failed         int                      `json:"failed"`
	Current        string                   `json:"current"`
	Failures       []Failure                `json:"failures"`
}

func (c *Console) resolve(ctx context.Context, id int64) (string, error) {
	return c.resolveNode(ctx, id, "hkg")
}

func (c *Console) resolveNode(ctx context.Context, id int64, upstream string) (string, error) {
	entry := c.apiBase + "/Api/Songs/play?node=nya"
	if upstream == "cf" {
		entry = c.apiBase + "/Api/Songs/play?node=cf"
	}
	clients := c.operationClients(upstreamstate.PlaybackURL, upstreamstate.Constraints{Route: upstream, Entry: entry})
	var failures []error
	for _, client := range clients {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
		target, err := c.resolveNodeWithClient(attempt, id, upstream, client)
		cancel()
		if err == nil {
			return target, nil
		}
		failures = append(failures, err)
	}
	return "", errors.Join(failures...)
}

func (c *Console) resolveNodeWithClient(ctx context.Context, id int64, upstream string, client *http.Client) (string, error) {
	started := time.Now()
	revision := upstreamrequest.Default.Snapshot().Revision
	node := "nya"
	if upstream == "cf" {
		node = "cf"
	}
	r, err := http.NewRequestWithContext(ctx, "GET", c.apiBase+"/Api/Songs/play?node="+node+"&id="+strconv.FormatInt(id, 10), nil)
	if err != nil {
		return "", applog.SafeError(err)
	}
	resp, err := client.Do(r)
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
	if (u.Scheme != "http" && u.Scheme != "https") || !videometa.ValidHost(u.Host) || u.User != nil || u.Fragment != "" {
		return "", errors.New("歌曲返回了无效的视频域名或协议")
	}
	if err := cacheproxy.ValidateVideoURL(u.String(), cacheproxy.DefaultConfig().MaxFileBytes); err != nil {
		return "", fmt.Errorf("歌曲返回了无效的视频地址：%w", err)
	}
	upstreamrequest.Default.ObserveResourceSourcesAtRevision(revision, "playback/"+node+"/"+strconv.FormatInt(id, 10), []upstreamrequest.ResourceSource{{API: r.URL.String(), Node: node, SongID: id, ResourceURL: u.String(), ObservedAt: time.Now()}}, 10*time.Minute)
	c.mu.Lock()
	engine := c.service
	c.mu.Unlock()
	if engine != nil {
		if err := engine.ObserveSongURL(ctx, cacheproxy.SongURL{SongID: id, URL: u.String(), API: c.apiBase + "/Api/Songs/play", Node: node, QueryStartedAt: started, ObservedAt: time.Now()}); err != nil {
			return "", fmt.Errorf("保存歌曲地址：%w", err)
		}
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
	c.batch = Batch{Running: true, ScanOnly: scanOnly, Phase: "正在检查远端并更新本地清单"}
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

	fail := func(message string) { c.mu.Lock(); c.batch.Phase = message; c.mu.Unlock() }
	refreshErr := c.refreshLocalCatalog(ctx, s)
	local, release, err := s.ReadLocalCatalog(ctx)
	defer release()
	if err != nil {
		fail("读取本地清单失败：" + err.Error())
		return
	}
	if local.Status.Revision == "" || len(local.Songs) == 0 {
		message := "本地尚无有效 MD5 清单，无法开始处理"
		if refreshErr != nil {
			message += "：" + refreshErr.Error()
		}
		fail(message)
		return
	}
	c.mu.Lock()
	c.batch.Catalog = local.Status
	if refreshErr != nil {
		c.batch.CatalogWarning = "远端检查失败，使用本地清单：" + refreshErr.Error()
	}
	c.batch.Phase = "正在检查本地清单与缓存文件"
	c.mu.Unlock()
	songs := local.Songs
	titles := map[string]string{}
	for _, song := range songs {
		name := ""
		if song.Name != nil {
			name = *song.Name
		}
		titles[strconv.FormatInt(song.ID, 10)] = name
	}
	check := local.Files
	type resourceJob struct {
		md5   string
		ids   []string
		score float64
	}
	grouped := map[string]*resourceJob{}
	total := 0
	for id, key := range check.References {
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
	// Retained mappings still contribute to shared-resource priority.
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
				currentI := local.Current[candidates[i]]
				currentJ := local.Current[candidates[j]]
				return currentI && !currentJ
			})
			for _, id := range candidates {
				n, _ := strconv.ParseInt(id, 10, 64)
				c.mu.Lock()
				c.batch.Current = id + " · " + titles[id]
				c.mu.Unlock()
				source, err = c.prefetchSong(songCtx, s, n, nil)
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
