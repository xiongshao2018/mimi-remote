//go:build darwin

package hostmetrics

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// darwin 采集全部走系统命令（vm_stat/sysctl/diskutil/ioreg），
// 与 hostinfo 包读 ComputerName 的方式一致；命令缺失时字段留空。

const darwinCommandTimeout = 3 * time.Second

func runDarwinCommand(ctx context.Context, name string, args ...string) string {
	runCtx, cancel := context.WithTimeout(ctx, darwinCommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(runCtx, name, args...).Output()
	if err != nil || len(output) == 0 || len(output) > 1<<20 {
		return ""
	}
	return string(output)
}

// darwin 没有 /proc/stat 的累计计数；readCPUCounters 用 top 的瞬时
// idle 百分比换算成单调递增的「虚拟计数」，让 Sampler 的差值协议
// （1 - idleDelta/totalDelta）仍然成立：每次调用 total 固定 +1000，
// idle 按当次 idle 百分比累加，差值即相邻两次采样间的平均占用。
var (
	darwinCPUAccumTotal uint64
	darwinCPUAccumIdle  uint64
)

func readCPUCounters() (total, idle uint64, ok bool) {
	out := runDarwinCommand(context.Background(), "top", "-l", "1", "-n", "0")
	if out == "" {
		return 0, 0, false
	}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		_, rest, found := strings.Cut(line, "CPU usage:")
		if !found {
			continue
		}
		// 形如 "12.34% user, 5.67% sys, 76.99% idle"
		var idlePct float64
		for _, part := range strings.Split(rest, ",") {
			part = strings.TrimSpace(part)
			if strings.HasSuffix(part, "% idle") {
				var err error
				idlePct, err = strconv.ParseFloat(strings.TrimSuffix(part, "% idle"), 64)
				if err != nil {
					return 0, 0, false
				}
			}
		}
		darwinCPUAccumTotal += 1000
		darwinCPUAccumIdle += uint64(idlePct * 10)
		return darwinCPUAccumTotal, darwinCPUAccumIdle, true
	}
	return 0, 0, false
}

func readMemory() (used, total uint64, ok bool) {
	ctx := context.Background()
	out := runDarwinCommand(ctx, "sysctl", "-n", "hw.memsize")
	if out == "" {
		return 0, 0, false
	}
	total, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
	if err != nil || total == 0 {
		return 0, 0, false
	}
	// 现代 macOS 的可用内存口径是 sysctl vm.page_free_count + speculative 等，
	// 但最贴近活动监视器的是 `memory_pressure` 的 free percentage。退化为
	// vm_stat 的 free+inactive 口径（与历史工具一致）。
	out = runDarwinCommand(ctx, "vm_stat")
	if out == "" {
		return 0, total, false
	}
	pageSize := uint64(16384)
	if outPS := runDarwinCommand(ctx, "sysctl", "-n", "hw.pagesize"); outPS != "" {
		if v, err := strconv.ParseUint(strings.TrimSpace(outPS), 10, 64); err == nil && v > 0 {
			pageSize = v
		}
	}
	var free, inactive, speculative uint64
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "."))
		v, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			continue
		}
		switch strings.TrimSpace(key) {
		case "Pages free":
			free = v
		case "Pages inactive":
			inactive = v
		case "Pages speculative":
			speculative = v
		}
	}
	available := (free + inactive + speculative) * pageSize
	if available > total {
		available = total
	}
	return total - available, total, true
}

func readNetCounters() (rx, tx uint64, ok bool) {
	out := runDarwinCommand(context.Background(), "netstat", "-ibn")
	if out == "" {
		return 0, 0, false
	}
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// 形如 "en0 1500 14400 9a:.. <Link> # bytes in ... bytes out"；
		// 简化口径：取每块网卡第一行的收/发字节列（列位随版本漂移，
		// 用表头定位）。
		if len(fields) == 0 {
			continue
		}
		if strings.EqualFold(fields[0], "Name") {
			continue
		}
		name := fields[0]
		if !strings.HasPrefix(name, "en") || seen[name] || len(fields) < 10 {
			continue
		}
		// netstat -ibn 的 Ibytes 在第 7 列、Obytes 在第 10 列（0 基 6/9），
		// 不同 macOS 版本列数不同；从行尾向前找两个数字更稳。
		var nums []uint64
		for _, f := range fields {
			if v, err := strconv.ParseUint(f, 10, 64); err == nil {
				nums = append(nums, v)
			}
		}
		if len(nums) < 2 {
			continue
		}
		seen[name] = true
		rx += nums[0]
		tx += nums[len(nums)-1]
		ok = true
	}
	return rx, tx, ok
}

func readRootDisk() (used, total uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return 0, 0, false
	}
	bsize := uint64(st.Bsize)
	total = st.Blocks * bsize
	free := st.Bavail * bsize
	if total == 0 || total < free {
		return 0, 0, false
	}
	return total - free, total, true
}

func collectHardware() Hardware {
	ctx := context.Background()
	hw := Hardware{OS: "macos"}
	if name, err := os.Hostname(); err == nil {
		hw.Hostname = name
	}
	if out := runDarwinCommand(ctx, "sw_vers", "-productVersion"); out != "" {
		hw.OSVersion = "macOS " + strings.TrimSpace(out)
	}
	if out := runDarwinCommand(ctx, "sysctl", "-n", "machdep.cpu.brand_string"); out != "" {
		hw.CPUModel = strings.TrimSpace(out)
	}
	if out := runDarwinCommand(ctx, "sysctl", "-n", "hw.ncpu"); out != "" {
		if v, err := strconv.Atoi(strings.TrimSpace(out)); err == nil {
			hw.CPUCores = v
		}
	}
	hw.GPUModel = readDarwinGPU()
	if _, total, ok := readMemory(); ok {
		hw.TotalMemory = total
	}
	hw.Disks = readDarwinDisks()
	return hw
}

func readDarwinGPU() string {
	out := runDarwinCommand(context.Background(), "system_profiler", "SPDisplaysDataType")
	if out == "" {
		return ""
	}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if model, found := strings.CutPrefix(line, "Chipset Model:"); found {
			return strings.TrimSpace(model)
		}
	}
	return ""
}

func readDarwinDisks() []Disk {
	var disks []Disk
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return nil
	}
	bsize := uint64(st.Bsize)
	total := st.Blocks * bsize
	free := st.Bavail * bsize
	if total > 0 && total >= free {
		disks = append(disks, Disk{Name: "/", Total: total, Free: free})
	}
	return disks
}
