//go:build windows

package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type cpuSample struct {
	AtMS       float64 `json:"atMS"`
	UserMS     float64 `json:"userMS"`
	KernelMS   float64 `json:"kernelMS"`
	Bytes      int64   `json:"bytes"`
	Goroutines int     `json:"goroutines"`
}
type cpuReport struct {
	ID          string        `json:"id"`
	Kind        string        `json:"kind"`
	Target      int           `json:"target"`
	PID         int           `json:"pid"`
	LogicalCPUs int           `json:"logicalCPUs"`
	GOMAXPROCS  int           `json:"gomaxprocs"`
	Host        string        `json:"host"`
	IP          string        `json:"ip"`
	Song        int           `json:"song"`
	Planned     []cpuStage    `json:"planned"`
	Stages      []cpuStage    `json:"stages"`
	StopMS      float64       `json:"stopMS"`
	EndMS       float64       `json:"endMS"`
	Samples     []cpuSample   `json:"samples"`
	Requests    []observation `json:"requests"`
	Peak        int           `json:"peak"`
	OK          bool          `json:"ok"`
	Error       string        `json:"error,omitempty"`
	AllocBytes  uint64        `json:"allocBytes"`
	GCs         uint32        `json:"gcs"`
	GCPauseMS   float64       `json:"gcPauseMS"`
	CoreMasks   []uint64      `json:"coreMasks"`
}

var advapiCPU = windows.NewLazySystemDLL("advapi32.dll")
var eventRegisterCPU = advapiCPU.NewProc("EventRegister")
var eventWriteCPU = advapiCPU.NewProc("EventWriteString")
var eventUnregisterCPU = advapiCPU.NewProc("EventUnregister")

func cpuMarker() (func(string) error, func(), error) {
	guid, err := windows.GUIDFromString("{E3D074A8-25FB-4D47-9427-3BC102CF5D31}")
	if err != nil {
		return nil, nil, err
	}
	var handle uint64
	code, _, _ := eventRegisterCPU.Call(uintptr(unsafe.Pointer(&guid)), 0, 0, uintptr(unsafe.Pointer(&handle)))
	if code != 0 {
		return nil, nil, fmt.Errorf("EventRegister status %d", code)
	}
	return func(value string) error {
		p, err := windows.UTF16PtrFromString(value)
		if err != nil {
			return err
		}
		code, _, _ := eventWriteCPU.Call(uintptr(handle), 4, 1, uintptr(unsafe.Pointer(p)))
		if code != 0 {
			return fmt.Errorf("EventWriteString status %d", code)
		}
		return nil
	}, func() { eventUnregisterCPU.Call(uintptr(handle)) }, nil
}
func cpuTimes() (float64, float64, error) {
	var creation, exit, kernel, user windows.Filetime
	err := windows.GetProcessTimes(windows.CurrentProcess(), &creation, &exit, &kernel, &user)
	cv := func(f windows.Filetime) float64 {
		return float64(uint64(f.HighDateTime)<<32|uint64(f.LowDateTime)) / 10000
	}
	return cv(user), cv(kernel), err
}

func cpuCoreMasks() ([]uint64, error) {
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetLogicalProcessorInformationEx")
	var size uint32
	proc.Call(0, 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return nil, errors.New("CPU topology unavailable")
	}
	b := make([]byte, size)
	ok, _, err := proc.Call(0, uintptr(unsafe.Pointer(&b[0])), uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		return nil, err
	}
	var masks []uint64
	for p := 0; p < int(size); {
		if p+48 > int(size) {
			return nil, errors.New("invalid CPU topology record")
		}
		n := int(binary.LittleEndian.Uint32(b[p+4:]))
		groups := binary.LittleEndian.Uint16(b[p+30:])
		if n < 48 || p+n > int(size) || groups != 1 || binary.LittleEndian.Uint16(b[p+40:]) != 0 {
			return nil, errors.New("CPU experiment supports one processor group")
		}
		masks = append(masks, binary.LittleEndian.Uint64(b[p+32:]))
		p += n
	}
	return masks, nil
}

func cpuTrial(parent context.Context, t target, kind string, n int, hold time.Duration, id, referencePath, out string) error {
	stages, duration, err := cpuSchedule(kind, n, hold)
	if err != nil {
		return err
	}
	var reference []string
	b, err := os.ReadFile(referencePath)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, &reference); err != nil {
		return err
	}
	if len(reference) != 96 || t.Size < 96*blockSize {
		return errors.New("CPU requires verified 96-block reference")
	}
	ctx, cancel := context.WithTimeout(parent, duration+20*time.Second)
	defer cancel()
	c, closeClient, err := clientFor(t)
	if err != nil {
		return err
	}
	defer closeClient()
	mark, unregister, err := cpuMarker()
	if err != nil {
		return err
	}
	defer unregister()
	r := cpuReport{ID: id, Kind: kind, Target: n, PID: os.Getpid(), LogicalCPUs: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0), Host: t.Host, IP: t.IP, Song: t.Song, Planned: stages, OK: true}
	r.CoreMasks, err = cpuCoreMasks()
	if err != nil {
		return err
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	state := &traceState{connections: make(map[net.Conn]int)}
	var next, completed atomic.Int64
	var stopping atomic.Bool
	var workers sync.WaitGroup
	var resultsMu sync.Mutex
	// Marker timestamps use the same QPC clock as kernel scheduling events.
	zero := time.Now()
	if err = mark("CFCPU|" + id + "|begin"); err != nil {
		return err
	}
	sample := func() error {
		u, k, e := cpuTimes()
		if e != nil {
			return e
		}
		r.Samples = append(r.Samples, cpuSample{ms(time.Since(zero)), u, k, completed.Load(), runtime.NumGoroutine()})
		return nil
	}
	if err = sample(); err != nil {
		return err
	}
	samplesDone := make(chan struct{})
	samplerStop := make(chan struct{})
	var sampleErr error
	go func() {
		defer close(samplesDone)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if e := sample(); e != nil {
					sampleErr = e
					return
				}
			case <-samplerStop:
				return
			}
		}
	}()
	launch := func() {
		workers.Go(func() {
			// Like the product Range worker, retain one block buffer per worker.
			buffer := make([]byte, blockSize)
			for !stopping.Load() && ctx.Err() == nil {
				i := next.Add(1) - 1
				if i >= 1024 {
					resultsMu.Lock()
					r.OK = false
					r.Error = "1 GiB trial traffic cap reached"
					resultsMu.Unlock()
					cancel()
					return
				}
				o := fetchBuffered(ctx, c, t, (i%96)*blockSize, blockSize, zero, state, reference, buffer)
				completed.Add(o.Bytes)
				resultsMu.Lock()
				r.Requests = append(r.Requests, o)
				if o.Error != "" {
					r.OK = false
					r.Error = "request failed; see observations"
				}
				resultsMu.Unlock()
				if o.Error != "" {
					cancel()
					return
				}
			}
		})
	}
	waitUntil := func(d time.Duration) bool {
		timer := time.NewTimer(max(0, d-time.Since(zero)))
		defer timer.Stop()
		select {
		case <-timer.C:
			return true
		case <-ctx.Done():
			return false
		}
	}
	running := 0
	for _, stage := range stages {
		if !waitUntil(time.Duration(stage.AtMS * float64(time.Millisecond))) {
			break
		}
		r.Stages = append(r.Stages, cpuStage{ms(time.Since(zero)), stage.N})
		if e := mark(fmt.Sprintf("CFCPU|%s|stage|%d", id, stage.N)); e != nil {
			err = e
			cancel()
			break
		}
		for running < stage.N {
			launch()
			running++
		}
	}
	waitUntil(duration)
	stopping.Store(true)
	r.StopMS = ms(time.Since(zero))
	mark("CFCPU|" + id + "|stop")
	workers.Wait()
	r.EndMS = ms(time.Since(zero))
	mark("CFCPU|" + id + "|end")
	close(samplerStop)
	<-samplesDone
	if e := sample(); e != nil {
		err = e
	}
	if sampleErr != nil {
		err = sampleErr
	}
	runtime.ReadMemStats(&after)
	r.AllocBytes = after.TotalAlloc - before.TotalAlloc
	r.GCs = after.NumGC - before.NumGC
	r.GCPauseMS = float64(after.PauseTotalNs-before.PauseTotalNs) / 1e6
	r.Peak = state.peak
	if ctx.Err() != nil || err != nil {
		r.OK = false
		if r.Error == "" {
			r.Error = "CPU trial interrupted or instrumentation failed"
		}
	}
	if e := save(out, r); e != nil {
		return e
	}
	fmt.Printf("id=%s kind=%s n=%d ok=%v active_s=%.3f drain_s=%.3f MiB=%.1f peak=%d\n", id, kind, n, r.OK, r.StopMS/1000, (r.EndMS-r.StopMS)/1000, float64(completed.Load())/float64(blockSize), r.Peak)
	if !r.OK {
		return errors.New(r.Error)
	}
	return nil
}
