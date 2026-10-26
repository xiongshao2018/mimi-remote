//go:build linux

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

// Linux 采集全部走 procfs 与 statfs，不依赖外部命令；
// 显卡型号是唯一例外（lspci 缺失时留空）。

func readCPUCounters() (total, idle uint64, ok bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	line := string(data[:min(len(data), 256)])
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	var sum, idleSum uint64
	for i, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			continue
		}
		sum += v
		// idle 是第 4 个计数（下标 3），iowait 是第 5 个（下标 4）；
		// 两者都算空闲，与 top/procps 的口径一致。
		if i == 3 || i == 4 {
			idleSum += v
		}
	}
	if sum == 0 {
		return 0, 0, false
	}
	return sum, idleSum, true
}

func readMemory() (used, total uint64, ok bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()

	var memTotal, memAvailable uint64
	var haveTotal, haveAvailable bool
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			memTotal, haveTotal = value*1024, true
		case "MemAvailable:":
			memAvailable, haveAvailable = value*1024, true
		}
		if haveTotal && haveAvailable {
			break
		}
	}
	if !haveTotal || !haveAvailable || memTotal < memAvailable {
		return 0, 0, false
	}
	return memTotal - memAvailable, memTotal, true
}

func readNetCounters() (rx, tx uint64, ok bool) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var first bool
	for scanner.Scan() {
		line := scanner.Text()
		if !first {
			first = true // 两行表头
			_ = scanner.Scan()
			continue
		}
		name, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "lo" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		rxBytes, err1 := strconv.ParseUint(fields[0], 10, 64)
		txBytes, err2 := strconv.ParseUint(fields[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		rx += rxBytes
		tx += txBytes
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
	hw := Hardware{OS: "linux"}
	if name, err := os.Hostname(); err == nil {
		hw.Hostname = name
	}
	hw.OSVersion = readOSReleaseVersion()
	hw.CPUModel, hw.CPUCores = readCPUInfo()
	hw.GPUModel = readGPUModel()
	if _, total, ok := readMemory(); ok {
		hw.TotalMemory = total
	}
	hw.Disks = readDisks()
	return hw
}

func readOSReleaseVersion() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), "=")
		if !found || key != "PRETTY_NAME" {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"`)
	}
	return ""
}

func readCPUInfo() (model string, cores int) {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return "", 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "model name" && model == "" {
			model = value
		}
		if key == "processor" {
			cores++
		}
	}
	return model, cores
}

func readGPUModel() string {
	runCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(runCtx, "lspci").Output()
	if err != nil || len(output) == 0 || len(output) > 1<<20 {
		return ""
	}
	for _, line := range strings.Split(string(output), "\n") {
		// 形如 "01:00.0 VGA compatible controller: NVIDIA Corporation GA104 [RTX 3070] (rev a1)"
		class, desc, found := strings.Cut(line, ": ")
		if !found {
			continue
		}
		if !strings.Contains(class, "VGA") && !strings.Contains(class, "3D") && !strings.Contains(class, "Display") {
			continue
		}
		if idx := strings.LastIndex(desc, "("); idx > 0 {
			desc = strings.TrimSpace(desc[:idx])
		}
		return desc
	}
	return ""
}

func readDisks() []Disk {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()

	seen := map[string]bool{}
	var disks []Disk
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		mountPoint, fstype := fields[1], fields[2]
		switch fstype {
		case "ext2", "ext3", "ext4", "xfs", "btrfs", "f2fs":
		default:
			continue
		}
		if seen[mountPoint] {
			continue
		}
		seen[mountPoint] = true
		var st syscall.Statfs_t
		if err := syscall.Statfs(mountPoint, &st); err != nil {
			continue
		}
		bsize := uint64(st.Bsize)
		total := st.Blocks * bsize
		free := st.Bavail * bsize
		if total == 0 || total < free {
			continue
		}
		disks = append(disks, Disk{Name: mountPoint, Total: total, Free: free})
		if len(disks) >= 4 {
			break
		}
	}
	return disks
}
