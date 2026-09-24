package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

func main() {
	url := flag.String("url", "", "")
	host := flag.String("host", "", "")
	workers := flag.Int("c", 20, "")
	duration := flag.Duration("d", 30*time.Second, "")
	flag.Parse()

	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: *workers}}
	var mu sync.Mutex
	total, failed := 0, 0
	statuses := map[string]int{}
	versions := map[string]int{}
	var failures []string
	deadline := time.Now().Add(*duration)
	start := time.Now()

	var wg sync.WaitGroup
	for range *workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				req, _ := http.NewRequest(http.MethodGet, *url, nil)
				if *host != "" {
					req.Host = *host
				}
				resp, err := client.Do(req)
				key, version := "", ""
				if err != nil {
					key = "error"
				} else {
					body, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					key = fmt.Sprint(resp.StatusCode)
					if f := strings.Fields(string(body)); len(f) > 0 && strings.HasPrefix(f[0], "version=") {
						version = f[0]
					}
				}
				mu.Lock()
				total++
				statuses[key]++
				if version != "" {
					versions[version]++
				}
				if key != "200" {
					failed++
					if len(failures) < 15 {
						detail := key
						if err != nil {
							detail = err.Error()
						}
						failures = append(failures, fmt.Sprintf("t+%.1fs %s", time.Since(start).Seconds(), detail))
					}
				}
				mu.Unlock()
				if key != "200" {
					time.Sleep(20 * time.Millisecond)
				}
			}
		}()
	}
	wg.Wait()

	keys := make([]string, 0, len(statuses))
	for k := range statuses {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("requests=%d failed=%d rps=%.0f\n", total, failed, float64(total)/duration.Seconds())
	for _, k := range keys {
		fmt.Printf("  status %s: %d\n", k, statuses[k])
	}
	for v, n := range versions {
		fmt.Printf("  %s: %d\n", v, n)
	}
	for _, f := range failures {
		fmt.Println("  fail", f)
	}
	if failed > 0 {
		os.Exit(1)
	}
}
