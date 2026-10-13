package main

import (
	"errors"
	"time"
)

type cpuStage struct {
	AtMS float64 `json:"atMS"`
	N    int     `json:"n"`
}

func cpuSchedule(kind string, n int, hold time.Duration) ([]cpuStage, time.Duration, error) {
	if n < 1 || n > 32 || hold < time.Second || hold > 10*time.Second {
		return nil, 0, errors.New("CPU bounds: 1..32 workers, 1..10 second hold")
	}
	var stages []cpuStage
	switch kind {
	case "idle":
		stages = []cpuStage{{0, 0}}
	case "burst":
		stages = []cpuStage{{0, n}}
	case "stagger":
		for i := 1; i <= n; i++ {
			stages = append(stages, cpuStage{float64((i - 1) * 250), i})
		}
	case "stepped":
		if n != 32 {
			return nil, 0, errors.New("stepped requires target 32")
		}
		for i, v := range []int{1, 4, 8, 12, 16, 24, 32} {
			stages = append(stages, cpuStage{ms(time.Duration(i) * hold), v})
		}
	default:
		return nil, 0, errors.New("unknown CPU schedule")
	}
	return stages, time.Duration(stages[len(stages)-1].AtMS*float64(time.Millisecond)) + hold, nil
}
