// Command benchsum turns the artifacts of .github/workflows/bench.yml into a
// Markdown table: one row per run, and per editor count the median p99 over
// its runs. Runs whose load generator used more than 70% of its two cores
// are marked invalid (docs/BENCHMARKS.md): the loadgen, not the server,
// may have been the bottleneck. The commit p99 column shows when the
// runner's disk, not the server, set the tail: a group commit waits for fsync.
//
//	go run ./cmd/benchsum results/
package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type summary struct {
	Editors   int     `json:"editors"`
	Connected int     `json:"connected"`
	Failed    int     `json:"failed"`
	Late      int     `json:"late"`
	OpsSent   int64   `json:"ops_sent"`
	Samples   int64   `json:"latency_samples"`
	P50       float64 `json:"p50_ms"`
	P90       float64 `json:"p90_ms"`
	P99       float64 `json:"p99_ms"`
	P999      float64 `json:"p99_9_ms"`
	Max       float64 `json:"max_ms"`
	ElapsedS  float64 `json:"elapsed_s"`
}

type row struct {
	name                string
	run                 int
	s                   summary
	loadgenCPU          float64 // percent of its 2 cores
	nodeCPU, pgCPU      float64 // mean docker stats CPU %, 100 = one core
	nodeMem             string  // last sample
	loadgenMem          string  // max RSS
	kicked              float64 // slow consumers kicked
	commitP99           string  // upper bound of the bucket holding the p99 group commit
	cpuModel            string
	invalid, incomplete string
}

const loadgenCores = 2

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: benchsum <results dir>")
		os.Exit(2)
	}
	dirs, _ := filepath.Glob(filepath.Join(os.Args[1], "*-*"))
	var rows []row
	for _, d := range dirs {
		if r, ok := read(d); ok {
			rows = append(rows, r)
		}
	}
	slices.SortFunc(rows, func(a, b row) int {
		if a.s.Editors != b.s.Editors {
			return a.s.Editors - b.s.Editors
		}
		return a.run - b.run
	})

	fmt.Println("| Editors | Run | p50 ms | p90 ms | p99 ms | p99.9 ms | max ms | Samples | Ops sent | Node CPU | Postgres CPU | Node memory | Slow clients kicked | Loadgen CPU | Loadgen memory | Commit p99 | CPU | Notes |")
	fmt.Println("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|")
	for _, r := range rows {
		notes := strings.TrimSpace(r.invalid + " " + r.incomplete)
		fmt.Printf("| %d | %d | %.1f | %.1f | %.1f | %.1f | %.1f | %d | %d | %.0f%% | %.0f%% | %s | %.0f | %.0f%% | %s | %s | %s | %s |\n",
			r.s.Editors, r.run, r.s.P50, r.s.P90, r.s.P99, r.s.P999, r.s.Max, r.s.Samples, r.s.OpsSent,
			r.nodeCPU, r.pgCPU, r.nodeMem, r.kicked, r.loadgenCPU, r.loadgenMem, r.commitP99, r.cpuModel, notes)
	}

	fmt.Println()
	fmt.Println("| Editors | Valid runs | Median p99 ms | p99 of each valid run |")
	fmt.Println("|---|---|---|---|")
	byEditors := map[int][]float64{}
	var counts []int
	for _, r := range rows {
		if _, seen := byEditors[r.s.Editors]; !seen {
			counts = append(counts, r.s.Editors)
			byEditors[r.s.Editors] = nil
		}
		if r.invalid == "" && r.incomplete == "" {
			byEditors[r.s.Editors] = append(byEditors[r.s.Editors], r.s.P99)
		}
	}
	for _, n := range counts {
		p := byEditors[n]
		slices.Sort(p)
		med := "–"
		if len(p) > 0 {
			med = fmt.Sprintf("%.1f", p[len(p)/2])
		}
		each := make([]string, len(p))
		for i, v := range p {
			each[i] = fmt.Sprintf("%.1f", v)
		}
		fmt.Printf("| %d | %d | %s | %s |\n", n, len(p), med, strings.Join(each, ", "))
	}
}

func read(dir string) (row, bool) {
	var r row
	r.name = filepath.Base(dir)
	_, runStr, _ := strings.Cut(r.name, "-")
	r.run, _ = strconv.Atoi(runStr)

	data, err := os.ReadFile(filepath.Join(dir, "loadgen.json"))
	if err != nil {
		return r, false
	}
	var lg struct {
		Summary summary `json:"summary"`
	}
	if json.Unmarshal(data, &lg) != nil {
		return r, false
	}
	r.s = lg.Summary
	if r.s.Failed > 0 || r.s.Late > 0 {
		r.incomplete = fmt.Sprintf("incomplete: %d failed, %d late", r.s.Failed, r.s.Late)
	}

	if t, err := os.ReadFile(filepath.Join(dir, "time.txt")); err == nil {
		// /usr/bin/time -f '%e %U %S %M': elapsed, user, system seconds, max RSS KB.
		f := strings.Fields(lastLine(string(t)))
		if len(f) == 4 {
			if kb, err := strconv.ParseFloat(f[3], 64); err == nil {
				r.loadgenMem = fmt.Sprintf("%.0fMiB", kb/1024)
			}
			f = f[:3]
		}
		if len(f) == 3 {
			e, _ := strconv.ParseFloat(f[0], 64)
			u, _ := strconv.ParseFloat(f[1], 64)
			s, _ := strconv.ParseFloat(f[2], 64)
			if e > 0 {
				r.loadgenCPU = (u + s) / (e * loadgenCores) * 100
			}
		}
	}
	if r.loadgenCPU > 70 {
		r.invalid = "invalid: loadgen CPU over 70%"
	}

	r.nodeCPU, r.nodeMem = dockerStats(filepath.Join(dir, "docker-stats.csv"), "node-1")
	r.pgCPU, _ = dockerStats(filepath.Join(dir, "docker-stats.csv"), "postgres")
	after := metrics(filepath.Join(dir, "metrics-after.txt"))
	before := metrics(filepath.Join(dir, "metrics-before.txt"))
	r.kicked = after[`clients_kicked_total{reason="slow_consumer"}`] - before[`clients_kicked_total{reason="slow_consumer"}`]
	r.commitP99 = quantile(after, before, "board_commit_duration_seconds", 0.99)

	if s, err := os.ReadFile(filepath.Join(dir, "setup.txt")); err == nil {
		for line := range strings.SplitSeq(string(s), "\n") {
			if v, ok := strings.CutPrefix(line, "Model name:"); ok {
				r.cpuModel = strings.TrimSpace(v)
			}
		}
	}
	return r, true
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// dockerStats averages the CPU % of the container whose name contains name,
// over samples while it was busy (the first and last samples bracket the run),
// and returns its last memory reading.
func dockerStats(path, name string) (float64, string) {
	f, err := os.Open(path)
	if err != nil {
		return 0, ""
	}
	defer func() { _ = f.Close() }()
	var sum float64
	var n int
	var mem string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), ",")
		if len(parts) != 3 || !strings.Contains(parts[0], name) {
			continue
		}
		cpu, err := strconv.ParseFloat(strings.TrimSuffix(parts[1], "%"), 64)
		if err != nil {
			continue
		}
		sum += cpu
		n++
		mem, _, _ = strings.Cut(parts[2], " /")
	}
	if n == 0 {
		return 0, ""
	}
	return sum / float64(n), mem
}

// quantile returns "≤ N ms": the upper bound of the histogram bucket that
// holds quantile q of the observations made between the two scrapes.
func quantile(after, before map[string]float64, name string, q float64) string {
	type bucket struct{ le, n float64 }
	var bs []bucket
	for k, v := range after {
		le, ok := strings.CutPrefix(k, name+`_bucket{le="`)
		if !ok {
			continue
		}
		le = strings.TrimSuffix(le, `"}`)
		f, err := strconv.ParseFloat(le, 64)
		if err != nil || le == "+Inf" {
			continue
		}
		bs = append(bs, bucket{f, v - before[k]})
	}
	slices.SortFunc(bs, func(a, b bucket) int { return cmp.Compare(a.le, b.le) })
	total := after[name+"_count"] - before[name+"_count"]
	if total <= 0 {
		return ""
	}
	for _, b := range bs {
		if b.n >= q*total {
			return fmt.Sprintf("≤ %.1f ms", b.le*1000)
		}
	}
	return fmt.Sprintf("> %.0f ms", bs[len(bs)-1].le*1000)
}

// metrics reads a Prometheus text exposition into series → value.
func metrics(path string) map[string]float64 {
	out := map[string]float64{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		if i < 0 {
			continue
		}
		if v, err := strconv.ParseFloat(line[i+1:], 64); err == nil {
			out[line[:i]] = v
		}
	}
	return out
}
