package console

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
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
	Skipped        int                      `json:"skipped"`
	Current        string                   `json:"current"`
	Failures       []Failure                `json:"failures"`
}

func (c *Console) resolve(ctx context.Context, id int64) (string, error) {
	return c.resolveNode(ctx, id, "hkg")
}

func (c *Console) resolveNode(ctx context.Context, id int64, upstream string) (string, error) {
	c.mu.Lock()
	engine := c.service
	c.mu.Unlock()
	if engine != nil {
		if err := engine.StorageError(); err != nil {
			return "", err
		}
	}

	entry := c.apiBase + "/Api/Songs/play?node=nya"
	if upstream == "cf" {
		entry = c.apiBase + "/Api/Songs/play?node=cf"
	}
	clients := c.operationClients(upstreamstate.PlaybackURL, upstreamstate.Constraints{Route: upstream, Entry: entry})
	var failures []error
	for i, client := range clients {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		attempt, cancel := channelAttempt(ctx, 10*time.Second, len(clients)-i)
		target, err := c.resolveNodeWithClient(attempt, id, upstream, client)
		cancel()
		if err == nil {
			return target, nil
		}
		if errors.Is(err, cacheproxy.ErrLocalStorage) {
			return "", err
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
	u, _, err := videometa.PlaybackLocation(resp, cacheproxy.DefaultConfig().MaxFileBytes)
	if err != nil {
		return "", applog.SafeError(err)
	}
	channelID := ""
	if channel, ok := client.Transport.(interface{ ChannelID() string }); ok {
		channelID = channel.ChannelID()
	}
	upstreamrequest.Default.ObserveResourceSourcesAtRevision(revision, "playback/"+node+"/"+strconv.FormatInt(id, 10), []upstreamrequest.ResourceSource{{API: r.URL.String(), APIChannelID: channelID, Node: node, SongID: id, ResourceURL: u.String(), ObservedAt: time.Now()}}, 10*time.Minute)
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
	allowMissingRoot := false
	if scanOnly {
		allowMissingRoot = !c.storageUsageKnown()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return errors.New("控制台正在退出")
	}
	if c.batch.Running {
		return errors.New("已有批量任务正在运行")
	}
	if scanOnly {
		c.mu.Unlock()
		_, err := os.ReadDir(c.settings.StorageDir)
		c.mu.Lock()
		if err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("无法扫描目录 %s：%w", c.settings.StorageDir, err)
			}
			if !allowMissingRoot {
				return missingStorageRootError(c.settings.StorageDir, err)
			}
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
			if err := c.writeSnapshot("batch", settings.StorageDir, result); err != nil {
				result.Phase += "；保存失败，上次成功结果保留：" + err.Error()
			} else {
				c.mu.Lock()
				c.lastBatch = result
				c.mu.Unlock()
			}
		}
		if err := c.writeSnapshot("attempt", settings.StorageDir, result); err != nil {
			result.Phase += "；无法保存本次进度：" + err.Error()
		}
		c.mu.Lock()
		c.batch = result
		c.batchCancel = nil
		c.mu.Unlock()
		slog.Info("batch_finished", "scan_only", scanOnly, "reused", result.Reused, "completed", completed, "elapsed_ms", time.Since(started).Milliseconds(), "missing", result.Missing, "cancelled", ctx.Err() != nil, "total", result.Total, "checked", result.Checked, "hits", result.Hits, "downloaded", result.Downloaded, "failed", result.Failed, "phase", result.Phase)
	}()

	fail := func(message string) { c.mu.Lock(); c.batch.Phase = message; c.mu.Unlock() }
	refreshErr := c.refreshLocalCatalog(ctx, s)
	local, release, err := s.ReadLocalCatalog(ctx)
	defer release()
	c.mu.Lock()
	if refreshErr != nil {
		c.batch.CatalogWarning = refreshErr.Error()
	}
	if err == nil && local.Status.Revision != "" && len(local.Songs) > 0 {
		c.batch.Catalog = local.Status
	}
	c.mu.Unlock()
	if err != nil {
		fail("读取本地清单失败：" + err.Error())
		return
	}
	if local.Status.Revision == "" || len(local.Songs) == 0 {
		fail("暂无可用的歌曲清单，未开始本地检查。请重试在线更新。")
		return
	}
	c.mu.Lock()
	c.batch.Phase = "正在检查本地清单与本地视频文件"
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
	process := func(ctx context.Context, job *resourceJob) {
		if ctx.Err() != nil {
			return
		}
		var err error
		ids := append([]string(nil), job.ids...)
		skip := func(id string) {
			ids = slices.DeleteFunc(ids, func(candidate string) bool { return candidate == id })
			c.mu.Lock()
			c.batch.Checked++
			c.batch.Skipped++
			c.mu.Unlock()
		}
		if !scanOnly {
			var policies map[int64]cacheproxy.SongPolicy
			policies, err = s.SongPolicies(ctx)
			favorite := false
			if err == nil {
				for _, id := range job.ids {
					n, _ := strconv.ParseInt(id, 10, 64)
					if policies[n] == cacheproxy.SongBlocked {
						skip(id)
					}
					favorite = favorite || settings.RetainFavorites && policies[n] == cacheproxy.SongFavorite
				}
			}
			if len(ids) == 0 {
				return
			}
			select {
			case <-budgetStop:
				if !favorite && err == nil {
					return
				}
			default:
			}
		}
		hit := check.Present[job.md5]
		source := "HIT"
		if err == nil && !hit && !scanOnly {
			songCtx, finish := cacheproxy.WithBatchSongBudget(ctx)
			defer finish()
			songCtx = cacheproxy.WithExpectedMD5(songCtx, job.md5)
			// Keep resource scheduling independent of which ID resolves it.
			// Prefer IDs still in the catalog, then try retained associations.
			candidates := append([]string(nil), ids...)
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
				result, attemptErr := c.prefetchSong(songCtx, s, n)
				if errors.Is(attemptErr, cacheproxy.ErrSongBlocked) {
					skip(id)
					continue
				}
				source, err = result, attemptErr
				if err == nil || songCtx.Err() != nil || errors.Is(err, cacheproxy.ErrBatchBudget) || errors.Is(err, cacheproxy.ErrLocalStorage) {
					break
				}
			}
			if len(ids) == 0 {
				return
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
		c.batch.Checked += len(ids)
		switch {
		case err != nil:
			c.batch.Failed += len(ids)
			for _, id := range ids {
				n, _ := strconv.ParseInt(id, 10, 64)
				c.batch.Failures = append(c.batch.Failures, Failure{ID: n, Name: titles[id], Error: err.Error()})
			}
		case hit:
			c.batch.Hits += len(ids)
			c.batch.Reused += len(ids)
			c.batch.CatalogHits += len(ids)
		case scanOnly:
			c.batch.Missing += len(ids)
		case source == "HIT":
			c.batch.Hits += len(ids)
		default:
			c.batch.Downloaded += len(ids)
		}
	}
	var wg sync.WaitGroup
dispatch:
	for _, job := range jobs {
		select {
		case <-ctx.Done():
			break dispatch
		default:
		}
		jobCtx, admitted := cacheproxy.WithPreparationAdmission(ctx)
		finished := make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(finished)
			process(jobCtx, job)
		}()
		// Resolve the next ordered resource as soon as this one owns usage
		// capacity. A local hit or early failure can finish without admission.
		select {
		case <-admitted:
		case <-finished:
		case <-ctx.Done():
			break dispatch
		}
	}
	wg.Wait()
	select {
	case <-budgetStop:
		c.mu.Lock()
		c.batch.BudgetReached = true
		c.batch.Current = ""
		c.batch.Phase = "视频存储预算不足，已停止普通补齐。"
		if settings.RetainFavorites {
			c.batch.Phase += "本轮可处理的收藏已继续处理。"
		}
		c.batch.Phase += "调整视频保留空间后可再次补齐。"
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
		c.batch.Phase = "本地检查完成；可通过下载补齐获取缺失视频"
	} else {
		c.batch.Phase = "所有已知歌曲已处理完成；同 MD5 共用一份视频"
	}
}
