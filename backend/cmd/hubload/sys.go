package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Host measurements from /proc (Linux). They attribute CPU to each process of the setup, so a
// report can say which of gateway, worker, Postgres, mockvendor or the generator ran out of CPU.

const clkTck = 100 // USER_HZ; 100 on every Linux build we run on

func loadAvg() [3]float64 {
	var out [3]float64
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return out
	}
	f := strings.Fields(string(b))
	for i := 0; i < 3 && i < len(f); i++ {
		out[i], _ = strconv.ParseFloat(f[i], 64)
	}
	return out
}

// procTicks is utime+stime of pid in clock ticks.
func procTicks(pid int) uint64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	s := string(b)
	// comm may contain spaces; fields after the closing parenthesis are fixed.
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 13 {
		return 0
	}
	u, _ := strconv.ParseUint(f[11], 10, 64)
	st, _ := strconv.ParseUint(f[12], 10, 64)
	return u + st
}

// matchTicks sums procTicks over every process whose comm equals comm (when set) and whose
// command line contains cmd (when set). Postgres backends carry "user database client" in
// their process title, so cmd " hub_load " selects one database's backends.
func matchTicks(comm, cmd string) uint64 {
	var total uint64
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	self := strconv.Itoa(os.Getpid()) // our own command line carries the match string (-cpu flag)
	for _, d := range dirs {
		if filepath.Base(d) == self {
			continue
		}
		if comm != "" {
			b, err := os.ReadFile(d + "/comm") //nolint:gosec // G304: /proc entries from a fixed glob
			if err != nil || strings.TrimSpace(string(b)) != comm {
				continue
			}
		}
		if cmd != "" {
			b, err := os.ReadFile(d + "/cmdline") //nolint:gosec // G304: /proc entries from a fixed glob
			if err != nil || !strings.Contains(strings.ReplaceAll(string(b), "\x00", " "), cmd) {
				continue
			}
		}
		pid, _ := strconv.Atoi(filepath.Base(d))
		t := procTicks(pid)
		total += t
	}
	return total
}

// machineTicks returns busy and total ticks of all CPUs from /proc/stat.
func machineTicks() (busy, total uint64) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}
	line, _, _ := strings.Cut(string(b), "\n")
	f := strings.Fields(line)
	for i, v := range f[1:] {
		n, _ := strconv.ParseUint(v, 10, 64)
		if i >= 8 { // guest time is already in user
			break
		}
		total += n
		if i != 3 && i != 4 { // idle, iowait
			busy += n
		}
	}
	return busy, total
}

// cpuTargets are named processes whose CPU use a run reports: name=pid or name=comm:<comm>.
type cpuTargets []cpuTarget

type cpuTarget struct {
	name string
	pid  int
	comm string // every process with this comm (e.g. all postgres processes of the cluster)
	cmd  string // every process whose command line contains this (e.g. " hub_load ": that database's backends)
}

func (c *cpuTargets) String() string { return fmt.Sprint(len(*c)) }

func (c *cpuTargets) Set(v string) error {
	for part := range strings.SplitSeq(v, ",") {
		name, ref, ok := strings.Cut(strings.TrimLeft(part, " "), "=") // keep trailing spaces: cmd: matches use them
		if !ok || name == "" {
			return fmt.Errorf("want name=pid or name=comm:<comm>, got %q", part)
		}
		if comm, isComm := strings.CutPrefix(ref, "comm:"); isComm {
			*c = append(*c, cpuTarget{name: name, comm: comm})
			continue
		}
		if cmd, isCmd := strings.CutPrefix(ref, "cmd:"); isCmd && cmd != "" {
			*c = append(*c, cpuTarget{name: name, cmd: cmd})
			continue
		}
		pid, err := strconv.Atoi(ref)
		if err != nil {
			return fmt.Errorf("bad pid in %q", part)
		}
		*c = append(*c, cpuTarget{name: name, pid: pid})
	}
	return nil
}

type cpuSnap struct {
	at        time.Time
	ticks     map[string]uint64
	busy, all uint64
}

func (c cpuTargets) snap() cpuSnap {
	s := cpuSnap{at: time.Now(), ticks: map[string]uint64{}}
	for _, t := range c {
		if t.comm != "" || t.cmd != "" {
			s.ticks[t.name] = matchTicks(t.comm, t.cmd)
			continue
		}
		s.ticks[t.name] = procTicks(t.pid)
	}
	s.ticks["hubload"] = procTicks(os.Getpid())
	s.busy, s.all = machineTicks()
	return s
}

// CPUUsage is CPU used during a window: cores per process (1.0 = one CPU fully busy) and the
// machine's busy share across all CPUs.
type CPUUsage struct {
	Cores       map[string]float64 `json:"cores"`
	MachineBusy float64            `json:"machine_busy"`
}

func cpuBetween(a, b cpuSnap) CPUUsage {
	secs := b.at.Sub(a.at).Seconds()
	u := CPUUsage{Cores: map[string]float64{}}
	if secs <= 0 {
		return u
	}
	for k, v := range b.ticks {
		u.Cores[k] = round3(float64(v-a.ticks[k]) / clkTck / secs)
	}
	if d := b.all - a.all; d > 0 {
		u.MachineBusy = round3(float64(b.busy-a.busy) / float64(d))
	}
	return u
}

// settle waits until the 1-minute load average is below threshold, polling every poll, for at
// most maxWait. It returns the load average it ended with.
func settle(ctx context.Context, threshold float64, poll, maxWait time.Duration, log io.Writer) [3]float64 {
	deadline := time.Now().Add(maxWait)
	for {
		la := loadAvg()
		if la[0] < threshold || time.Now().After(deadline) || ctx.Err() != nil {
			if la[0] >= threshold {
				fmt.Fprintf(log, "settle: load average %.2f still >= %.2f after %s; proceeding\n", la[0], threshold, maxWait)
			}
			return la
		}
		fmt.Fprintf(log, "settle: load average %.2f >= %.2f; waiting\n", la[0], threshold)
		t := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			t.Stop()
		case <-t.C:
		}
	}
}

func settleCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hubload settle", flag.ContinueOnError)
	fs.SetOutput(stderr)
	below := fs.Float64("below", 1.0, "1-minute load average to wait for")
	poll := fs.Duration("poll", 30*time.Second, "poll interval")
	maxWait := fs.Duration("max", 40*time.Minute, "give up and proceed after this long")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	la := settle(ctx, *below, *poll, *maxWait, stderr)
	fmt.Fprintf(stdout, "%.2f %.2f %.2f\n", la[0], la[1], la[2])
	return 0
}
