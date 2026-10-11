package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

func transportCount(url, name string) (int64, error) {
	tr := &http.Transport{Proxy: nil}
	defer tr.CloseIdleConnections()
	r, err := (&http.Client{Transport: tr, Timeout: time.Second}).Get(url)
	if err != nil {
		return 0, err
	}
	defer r.Body.Close()
	var counts map[string]int64
	if err := json.NewDecoder(r.Body).Decode(&counts); err != nil {
		return 0, err
	}
	n, ok := counts[name]
	if !ok {
		return 0, fmt.Errorf("missing transport counter")
	}
	return n, nil
}
