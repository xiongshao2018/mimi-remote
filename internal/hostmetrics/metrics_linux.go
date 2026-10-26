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

// readGPU 用 nvidia-smi 一次查询拿到型号与实时指标；命令缺失、没有 NVIDIA
// 显卡或查询失败时返回 OK=false。驱动自带的 nvidia-smi 是本机能拿到的最
// 直接来源，与 Strata 的 Monitor 页同源（后者走 NVML，口径一致）。
func readGPU() GPUMetrics {
	runCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// --query-gpu 一次返回，避免多次 exec；nounits 让数值不带单位后缀。
	output, err := exec.CommandContext(runCtx, "nvidia-smi",
		"--query-gpu=name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw,power.limit",
		"--format=csv,noheader,nounits").Output()
	if err != nil || len(output) == 0 || len(output) > 1<<16 {
		return GPUMetrics{}
	}
	line := strings.TrimSpace(string(output))
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx] // 多卡时只报第一块，避免字段语义混淆
	}
	fields := strings.Split(line, ",")
	if len(fields) < 7 {
		return GPUMetrics{}
	}
	for i := range fields {
		fields[i] = strings.TrimSpace(fields[i])
	}
	g := GPUMetrics{OK: true, Name: fields[0]}
	if v, err := strconv.ParseFloat(fields[1], 64); err == nil {
		g.UtilPercent = v
	}
	if v, err := strconv.ParseFloat(fields[2], 64); err == nil {
		g.MemUsed = uint64(v)
	}
	if v, err := strconv.ParseFloat(fields[3], 64); err == nil {
		g.MemTotal = uint64(v)
	}
	if v, err := strconv.ParseFloat(fields[4], 64); err == nil {
		g.TempC = v
	}
	if v, err := strconv.ParseFloat(fields[5], 64); err == nil {
		g.PowerW = v
	}
	if v, err := strconv.ParseFloat(fields[6], 64); err == nil {
		g.PowerLimitW = v
	}
	return g
}

// readDiskCounters 汇总所有物理块设备的读写扇区数（512 字节/扇区）。
// 部分设备（如 loop/ram）没有统计列，跳过即可。
func readDiskCounters() (readBytes, writeBytes uint64, ok bool) {
	f, err := os.Open("/proc/diskstats")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()

	var readSectors, writeSectors uint64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// 字段：major minor name read_ios ... read_sectors(6) ... write_sectors(10)
		if len(fields) < 14 {
			continue
		}
		if fields[2] == "loop0" || strings.HasPrefix(fields[2], "ram") {
			continue
		}
		if v, err := strconv.ParseUint(fields[5], 10, 64); err == nil {
			readSectors += v
		}
		if v, err := strconv.ParseUint(fields[9], 10, 64); err == nil {
			writeSectors += v
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, false
	}
	return readSectors * 512, writeSectors * 512, true
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
