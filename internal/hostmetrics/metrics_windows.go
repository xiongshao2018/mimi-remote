//go:build windows

package hostmetrics

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Windows 采集走 kernel32/iphlpapi API 与注册表，不依赖 wmic/powershell 子进程。
// GetSystemTimes 与 GlobalMemoryStatusEx 不在 x/sys 导出面内，用 LazyProc 直调。

var (
	modKernel32            = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes     = modKernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatus = modKernel32.NewProc("GlobalMemoryStatusEx")
)

// ifTypes 是 netioapi.h 的接口类型常量（x/sys 未导出）。
const (
	ifTypeEthernet = 6
	ifTypeIEEE80211 = 71
)

// filetimeTicks 把 FILETIME 拼成单调递增的 64 位 tick 计数。
func filetimeTicks(ft windows.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}

func readCPUCounters() (total, idle uint64, ok bool) {
	var idleFt, kernelFt, userFt windows.Filetime
	r1, _, _ := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idleFt)),
		uintptr(unsafe.Pointer(&kernelFt)),
		uintptr(unsafe.Pointer(&userFt)),
	)
	if r1 == 0 {
		return 0, 0, false
	}
	idleTicks := filetimeTicks(idleFt)
	kernelTicks := filetimeTicks(kernelFt)
	userTicks := filetimeTicks(userFt)
	// kernel 时间包含 idle（Windows 口径）。
	return kernelTicks + userTicks, idleTicks, true
}

// memoryStatusEx 对应 Windows MEMORYSTATUSEX；字段顺序与 C 布局一致。
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

func readMemory() (used, total uint64, ok bool) {
	var status memoryStatusEx
	status.length = uint32(unsafe.Sizeof(status))
	r1, _, _ := procGlobalMemoryStatus.Call(uintptr(unsafe.Pointer(&status)))
	if r1 == 0 || status.totalPhys == 0 || status.availPhys > status.totalPhys {
		return 0, 0, false
	}
	return status.totalPhys - status.availPhys, status.totalPhys, true
}

func readNetCounters() (rx, tx uint64, ok bool) {
	var tablePtr *windows.MibIfTable2
	if err := windows.GetIfTable2Ex(windows.MibIfTableNormal, &tablePtr); err != nil || tablePtr == nil {
		return 0, 0, false
	}
	defer windows.FreeMibTable(unsafe.Pointer(tablePtr))

	entrySize := unsafe.Sizeof(windows.MibIfRow2{})
	base := unsafe.Pointer(&tablePtr.Table[0])
	for i := 0; i < int(tablePtr.NumEntries); i++ {
		entry := (*windows.MibIfRow2)(unsafe.Add(base, uintptr(i)*entrySize))
		// 只统计以太网/WiFi 类在线接口，排除 loopback、隧道与虚拟网卡。
		if entry.Type != ifTypeEthernet && entry.Type != ifTypeIEEE80211 {
			continue
		}
		if entry.OperStatus != windows.IfOperStatusUp {
			continue
		}
		rx += entry.InOctets
		tx += entry.OutOctets
		ok = true
	}
	return rx, tx, ok
}

func readRootDisk() (used, total uint64, ok bool) {
	root, err := syscall.UTF16PtrFromString(`\`)
	if err != nil {
		return 0, 0, false
	}
	var freeAvailable, totalBytes, freeBytes uint64
	if err := windows.GetDiskFreeSpaceEx(root, &freeAvailable, &totalBytes, &freeBytes); err != nil {
		return 0, 0, false
	}
	if totalBytes == 0 || totalBytes < freeAvailable {
		return 0, 0, false
	}
	return totalBytes - freeAvailable, totalBytes, true
}

func collectHardware() Hardware {
	hw := Hardware{OS: "windows"}
	if name, err := os.Hostname(); err == nil {
		hw.Hostname = name
	}
	hw.OSVersion = readWindowsOSVersion()
	hw.CPUModel, hw.CPUCores = readWindowsCPU()
	hw.GPUModel = readWindowsGPU()
	if _, total, ok := readMemory(); ok {
		hw.TotalMemory = total
	}
	hw.Disks = readWindowsDisks()
	return hw
}

func readWindowsOSVersion() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()
	product, _, err := key.GetStringValue("ProductName")
	if err != nil || product == "" {
		return ""
	}
	display, _, _ := key.GetStringValue("DisplayVersion")
	if display == "" {
		display, _, _ = key.GetStringValue("CurrentBuildNumber")
	}
	if display != "" {
		return product + " " + display
	}
	return product
}

func readWindowsCPU() (model string, cores int) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err == nil {
		model, _, _ = key.GetStringValue("ProcessorNameString")
		key.Close()
	}
	model = strings.TrimSpace(model)
	cores = int(windows.GetActiveProcessorCount(0xFFFF))
	return model, cores
}

func readWindowsGPU() string {
	// 显示适配器类 GUID 下的设备键带 DriverDesc；取第一个非虚拟设备名。
	root, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}`, registry.READ)
	if err != nil {
		return ""
	}
	defer root.Close()
	names, err := root.ReadSubKeyNames(32)
	if err != nil {
		return ""
	}
	for _, name := range names {
		if !strings.HasPrefix(name, "0") {
			continue
		}
		sub, err := registry.OpenKey(root, name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		desc, _, err := sub.GetStringValue("DriverDesc")
		sub.Close()
		if err != nil || desc == "" {
			continue
		}
		if strings.Contains(desc, "RDP") || strings.Contains(desc, "Basic Render") ||
			strings.Contains(desc, "Microsoft") {
			continue
		}
		return desc
	}
	return ""
}

func readWindowsDisks() []Disk {
	var disks []Disk
	for letter := 'C'; letter <= 'Z'; letter++ {
		root := string(letter) + `:\`
		rootPtr, err := syscall.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		var freeAvailable, totalBytes, freeBytes uint64
		if err := windows.GetDiskFreeSpaceEx(rootPtr, &freeAvailable, &totalBytes, &freeBytes); err != nil {
			continue
		}
		if totalBytes == 0 || totalBytes < freeAvailable {
			continue
		}
		disks = append(disks, Disk{Name: filepath.Clean(root), Total: totalBytes, Free: freeBytes})
		if len(disks) >= 4 {
			break
		}
	}
	return disks
}
