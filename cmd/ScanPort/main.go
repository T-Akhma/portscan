package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func scanPort(host string, port int, timeout time.Duration) bool {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func worker(host string, ports <-chan int, results chan<- int, timeout time.Duration, wg *sync.WaitGroup) {
	defer wg.Done()
	for p := range ports {
		if scanPort(host, p, timeout) {
			results <- p
		}
	}
}

func parseSinglePort(token string) (int, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return 0, fmt.Errorf("empty port token")
	}
	n, err := strconv.Atoi(token)
	if err != nil {
		return 0, fmt.Errorf("invalid port %q", token)
	}
	if n >= 1 && n <= 65535 {
		return n, nil
	}
	return 0, fmt.Errorf("port out of range: %d", n)
}

func parsePortRange(token string) (int, int, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return 0, 0, fmt.Errorf("empty range token")
	}

	parts := strings.SplitN(token, "-", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid range %q (need A-B)", token)
	}

	left := strings.TrimSpace(parts[0])
	right := strings.TrimSpace(parts[1])
	if left == "" || right == "" {
		return 0, 0, fmt.Errorf("invalid range bounds in %q", token)
	}

	start, err := parseSinglePort(left)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid left bound %q: %w", left, err)
	}
	end, err := parseSinglePort(right)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid right bound %q: %w", right, err)
	}

	if start > end {
		return 0, 0, fmt.Errorf("range start > end in %q", token)
	}
	return start, end, nil
}

func expandPortSpec(spec string) ([]int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("empty ports spec")
	}
	tokens := strings.Split(spec, ",")

	singles := make([]int, 0, len(tokens))
	ranges := make([][2]int, 0, len(tokens))

	for i, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			return nil, fmt.Errorf("empty token at position %d", i)
		}

		if strings.Contains(tok, "-") {
			start, end, err := parsePortRange(tok)
			if err != nil {
				return nil, fmt.Errorf("invalid range %q: %w", tok, err)
			}
			ranges = append(ranges, [2]int{start, end})
		} else {
			n, err := parseSinglePort(tok)
			if err != nil {
				return nil, fmt.Errorf("invalid port %q: %w", tok, err)
			}
			singles = append(singles, n)
		}
	}

	seen := make(map[int]struct{}, len(singles))
	for _, n := range singles {
		seen[n] = struct{}{}
	}

	for _, rg := range ranges {
		start, end := rg[0], rg[1]
		for p := start; p <= end; p++ {
			seen[p] = struct{}{}
		}
	}

	ports := make([]int, 0, len(seen))
	for p := range seen {
		ports = append(ports, p)
	}
	sort.Ints(ports)

	return ports, nil
}

func main() {
	host := flag.String("host", "127.0.0.1", "target host/IP or domain")
	start := flag.Int("start", 1, "start port (1..65535)")
	end := flag.Int("end", 1024, "end port (1..65535)")
	timeout := flag.Duration("timeout", 500*time.Millisecond, "dial timeout (e.g. 300ms, 1s)")
	workers := flag.Int("workers", 100, "number of concurrent workers")
	quiet := flag.Bool("quiet", false, "suppress per-port lines; show only summary")
	asJSON := flag.Bool("json", false, "print JSON report only")
	resbuf := flag.Int("resbuf", 1024, "results channel buffer size")
	portspec := flag.String("ports", "", `ports/ranges, e.g. "22,80,443,8000-8100" (overrides -start/-end)`)
	flag.Parse()

	if *start < 1 || *start > 65535 {
		log.Fatalf("invalid start port: %d (must be 1..65535)", *start)
	}
	if *end < 1 || *end > 65535 {
		log.Fatalf("invalid end port: %d (must be 1..65535)", *end)
	}
	if *start > *end {
		log.Fatalf("start port (%d) must be <= end port (%d)", *start, *end)
	}
	if *workers <= 0 {
		log.Fatalf("workers must be > 0")
	}
	if *resbuf < 1 {
		log.Fatalf("resbuf must be >= 1 (got %d)", *resbuf)
	}

	var plan []int

	if *portspec != "" {
		ps, err := expandPortSpec(*portspec)
		if err != nil {
			log.Fatalf("ports: %v", err)
		}
		plan = ps
	} else {
		plan = make([]int, 0, *end-*start+1)
		for p := *start; p <= *end; p++ {
			plan = append(plan, p)
		}
	}

	if !*asJSON {
		fmt.Println("Portscanner config:")
		fmt.Printf("  host:    %s\n", *host)
		if *portspec != "" {
			fmt.Printf("  ports:   %s\n", *portspec)
		} else {
			fmt.Printf("  start:   %d\n", *start)
			fmt.Printf("  end:     %d\n", *end)
		}
		fmt.Printf("  timeout: %s\n", *timeout)
		fmt.Printf("  workers: %d\n", *workers)
		fmt.Printf("  resbuf:  %d\n", *resbuf)
		fmt.Printf("  targets: %d ports\n", len(plan))

	}

	startTime := time.Now()

	ports := make(chan int, *workers)
	results := make(chan int, *resbuf)

	var wg sync.WaitGroup
	wg.Add(*workers)
	for i := 0; i < *workers; i++ {
		go worker(*host, ports, results, *timeout, &wg)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	go func() {
		for _, p := range plan {
			ports <- p
		}
		close(ports)
	}()

	openPorts := make([]int, 0, 64)
	for p := range results {
		if !*asJSON && !*quiet {
			fmt.Printf("open: %d\n", p)
		}
		openPorts = append(openPorts, p)
	}

	sort.Ints(openPorts)
	duration := time.Since(startTime)

	if *asJSON {
		type ScanReport struct {
			Host       string `json:"host"`
			Start      int    `json:"start,omitempty"`
			End        int    `json:"end,omitempty"`
			PortsSpec  string `json:"ports_spec,omitempty"`
			Targets    int    `json:"targets"`
			Timeout    string `json:"timeout"`
			Workers    int    `json:"workers"`
			OpenPorts  []int  `json:"open_ports"`
			Count      int    `json:"count"`
			DurationMS int64  `json:"duration_ms"`
		}
		report := ScanReport{
			Host:       *host,
			Targets:    len(plan),
			Timeout:    (*timeout).String(),
			Workers:    *workers,
			OpenPorts:  openPorts,
			Count:      len(openPorts),
			DurationMS: duration.Milliseconds(),
		}
		if *portspec != "" {
			report.PortsSpec = *portspec
		} else {
			report.Start = *start
			report.End = *end
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			log.Fatalf("json encode failed: %v", err)
		}
		return
	}

	fmt.Printf("open ports: %d\n", len(openPorts))
	if *quiet && len(openPorts) > 0 {
		fmt.Printf("list: %v\n", openPorts)
	}
	fmt.Printf("scan duration: %s\n", duration)
}
