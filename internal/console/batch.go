package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
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
	r, err := http.NewRequestWithContext(ctx, "GET", c.apiBase+"/Api/Songs/play?node=nya&id="+strconv.FormatInt(id, 10), nil)
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
	parts := strings.Split(u.Path, "/")
	file := parts[len(parts)-1]
	if !strings.HasPrefix(file, strconv.FormatInt(id, 10)+"-") {
		return "", errors.New("视频地址的歌曲 ID 不匹配")
	}
	return u.String(), nil
}

func (c *Console) startBatch() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return errors.New("控制台正在退出")
	}
	if c.batch.Running {
		return errors.New("已有批量任务正在运行")
	}
	if c.queue.Running {
		return errors.New("请先停止队列预缓存")
	}
	if err := c.ensureEngine(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.batchCancel = cancel
	c.batchDone = make(chan struct{})
	c.batch = Batch{Running: true, Phase: "正在获取最新歌曲列表"}
	go c.runBatch(ctx, c.service, c.batchDone)
	return nil
}

func (c *Console) runBatch(ctx context.Context, s *cacheproxy.Server, done chan struct{}) {
	defer close(done)
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.batch.Running = false
		c.batchCancel = nil
		if ctx.Err() != nil {
			c.batch.Phase = "已停止排队；已开始的共享下载继续完成，退出控制台可全部取消"
		}
	}()
	songs, err := c.catalog(ctx)
	if err != nil {
		c.mu.Lock()
		c.batch.Phase = "获取歌曲列表失败：" + err.Error()
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	c.batch.Total = len(songs)
	c.batch.Phase = "正在校验并补齐"
	c.mu.Unlock()
	// One background worker leaves capacity for interactive game playback.
	for _, song := range songs {
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		c.batch.Current = fmt.Sprintf("%d · %s", song.ID, song.Name)
		c.mu.Unlock()
		target, err := c.resolve(ctx, song.ID)
		source := ""
		if err == nil {
			source, err = s.Prefetch(ctx, target)
		}
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		c.batch.Checked++
		if err != nil {
			c.batch.Failed++
			c.batch.Failures = append(c.batch.Failures, Failure{song.ID, song.Name, err.Error()})
		} else if source == "HIT" {
			c.batch.Hits++
		} else {
			c.batch.Downloaded++
		}
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
	}
	c.mu.Lock()
	c.batch.Current = ""
	if c.batch.Failed > 0 {
		c.batch.Phase = "检查完成，部分歌曲失败；再次检查可重试"
	} else {
		c.batch.Phase = "所有已知歌曲已校验并缓存"
	}
	c.mu.Unlock()
}
