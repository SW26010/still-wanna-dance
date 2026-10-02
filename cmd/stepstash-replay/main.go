// Offline parser replay. This command has no cache or network dependencies.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"stepstash/internal/legal"
	"stepstash/internal/vrclog"
)

func main() {
	showTerms := flag.Bool("show-terms", false, "print usage terms and exit")
	acceptTerms := flag.String("accept-terms", "", "explicitly accept the terms version shown by -show-terms")
	dir := flag.String("logs", "", "directory containing output_log_*.txt (read only)")
	flag.Parse()
	if *showTerms {
		fmt.Print(legal.Text)
		return
	}
	if err := legal.CheckAcceptance(*acceptTerms); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "-logs is required")
		os.Exit(2)
	}
	paths, err := filepath.Glob(filepath.Join(*dir, "output_log_*.txt"))
	if err != nil || len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "no log files found")
		os.Exit(1)
	}
	stats := struct{ Files, Lines, Snapshots, Empty, Changed, Repeated, Resets, Invalid, MaxQueue, UniqueSongs, NonCatalogEntries int }{}
	unique := map[int64]bool{}
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		stats.Files++
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 65536), 1<<20)
		var previous []int64
		for scanner.Scan() {
			stats.Lines++
			e, ok, err := vrclog.Parse(scanner.Text())
			if err != nil {
				stats.Invalid++
				previous = nil
				continue
			}
			if !ok {
				continue
			}
			if e.Reset {
				stats.Resets++
				previous = nil
				continue
			}
			stats.Snapshots++
			if len(e.Songs) == 0 {
				stats.Empty++
			}
			if len(e.Songs) > stats.MaxQueue {
				stats.MaxQueue = len(e.Songs)
			}
			ids := make([]int64, 0, len(e.Songs))
			for _, s := range e.Songs {
				ids = append(ids, s.ID)
				if s.ID > 0 {
					unique[s.ID] = true
				} else {
					stats.NonCatalogEntries++
				}
			}
			if reflect.DeepEqual(previous, ids) {
				stats.Repeated++
			} else {
				stats.Changed++
			}
			previous = ids
		}
		err = scanner.Err()
		f.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	stats.UniqueSongs = len(unique)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(stats)
	if stats.Invalid > 0 {
		os.Exit(1)
	}
}
