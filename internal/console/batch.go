package console

import (
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

	"stepstash/internal/cacheproxy"
)

type Song struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
type Failure struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Error string `json:"error"`
}
type Batch struct {
	ScanOnly   bool      `json:"scanOnly"`
	Missing    int       `json:"missing"`
	Updated    time.Time `json:"updated"`
	Finished   time.Time `json:"finished"`
	Running    bool      `json:"running"`
	Phase      string    `json:"phase"`
	Total      int       `json:"total"`
	Checked    int       `json:"checked"`
	Hits       int       `json:"hits"`
	Downloaded int       `json:"downloaded"`
	Failed     int       `json:"failed"`
	Current    string    `json:"current"`
	Failures   []Failure `json:"failures"`
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
		return nil, errors.New("歌曲列表为空或格式不受支持；未开始下载")
	}
	sort.Slice(songs, func(i, j int) bool { return songs[i].ID < songs[j].ID })
	return songs, nil
}

func (c *Console) catalog(ctx context.Context) ([]Song, error) {
	r, err := http.NewRequestWithContext(ctx, "GET", c.apiBase+"/Api/Songs/list", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("歌曲列表接口返回 %d", resp.StatusCode)
	}
	return parseCatalog(resp.Body)
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
		return "", err
	}
	resp, err := c.client.Do(r)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 302 && resp.StatusCode != 307 && resp.StatusCode != 301 {
		return "", fmt.Errorf("歌曲地址接口返回 %d", resp.StatusCode)
	}
	u, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" || (u.Host != "play.udon.dance" && u.Host != "nya.xin.moe") || u.User != nil || u.Fragment != "" {
		return "", errors.New("歌曲返回了未支持的视频地址；当前支持 CF/HKG HTTP")
	}
	if u.Host != host {
		return "", fmt.Errorf("%s 返回了其他上游的视频地址", upstream)
	}
	parts := strings.Split(u.Path, "/")
	file := parts[len(parts)-1]
	if !strings.HasPrefix(file, strconv.FormatInt(id, 10)+"-") {
		return "", errors.New("视频地址的歌曲 ID 不匹配")
	}
	return u.String(), nil
}

func (c *Console) startBatch() error { return c.startBatchMode(false) }

func (c *Console) startBatchMode(scanOnly bool) error {
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
		if _, err := os.ReadDir(c.settings.StorageDir); err != nil {
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
	c.batch = Batch{Running: true, ScanOnly: scanOnly, Phase: "正在获取最新歌曲列表"}
	slog.Info("batch_started")
	go c.runBatch(ctx, c.service, c.batchDone)
	return nil
}

func (c *Console) runBatch(ctx context.Context, s *cacheproxy.Server, done chan struct{}) {
	defer close(done)
	completed := false
	c.mu.Lock()
	scanOnly, settings := c.batch.ScanOnly, c.settings
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.batch.Running = false
		c.batchCancel = nil
		c.batch.Finished = time.Now()
		if ctx.Err() != nil {
			c.batch.Phase = "任务已停止，上次成功结果保留。已开始的共享下载可能继续完成。"
		}
		if completed && ctx.Err() == nil && c.batch.Total > 0 && c.batch.Checked == c.batch.Total && c.batch.Failed == 0 {
			c.batch.Updated = time.Now()
			if err := c.writeSnapshot("batch", settings, c.batch); err != nil {
				c.batch.Phase += "；保存失败，上次成功结果保留：" + err.Error()
			} else {
				c.lastBatch = c.batch
			}
		}
		if err := c.writeSnapshot("attempt", settings, c.batch); err != nil {
			c.batch.Phase += "；无法保存本次进度：" + err.Error()
		}
		slog.Info("batch_finished", "cancelled", ctx.Err() != nil, "total", c.batch.Total, "checked", c.batch.Checked, "hits", c.batch.Hits, "downloaded", c.batch.Downloaded, "failed", c.batch.Failed, "phase", c.batch.Phase)
	}()
	songs, err := c.catalog(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("catalog_failed", "error", err)
		}
		c.mu.Lock()
		c.batch.Phase = "获取歌曲列表失败：" + err.Error()
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	c.batch.Total = len(songs)
	slog.Info("catalog_loaded", "total", len(songs))
	c.batch.Phase = "正在下载补齐"
	if scanOnly {
		c.batch.Phase = "正在扫描校验（不下载）"
	}
	c.mu.Unlock()
	active := map[int64]string{}
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
		if ctx.Err() != nil {
			return
		}
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
		source := ""
		if scanOnly {
			var hit bool
			hit, err = scanner.check(ctx, func() (string, error) {
				return c.resolve(ctx, song.ID)
			}, func(target string) (bool, error) {
				return cacheproxy.CheckLocal(ctx, settings.StorageDir, target)
			})
			if hit {
				source = "HIT"
			} else {
				source = "MISSING"
			}
		} else {
			err = s.SetSongTitle(ctx, strconv.FormatInt(song.ID, 10), song.Name)
			if err == nil {
				source, err = c.prefetchSong(ctx, s, song.ID, nil)
			}
		}
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
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
		c.mu.Unlock()
		if scanOnly {
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
		case <-ctx.Done():
			break dispatch
		case jobs <- song:
		}
	}
	close(jobs)
	wg.Wait()
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
