// Package hostmetrics 采集 agentd 所在电脑的硬件配置与实时资源指标，
// 供 iOS 设备页（显卡/内存/硬盘摘要）与会话输入框指标条（CPU/内存/磁盘
// 用量、网络速度）展示。静态硬件信息进程内只采一次；实时指标需要两次
// 采样才能算出 CPU 占用与网络速率，采样间隔即速率窗口。
package hostmetrics

import (
	"context"
	"sync"
	"time"
)

// Disk 是一块逻辑磁盘的容量快照（字节）。
type Disk struct {
	Name  string `json:"name"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}

// Hardware 是一次性读取的静态硬件画像。字段读不到时留空/零值，
// 客户端按缺省处理，不阻塞其余字段展示。
type Hardware struct {
	OS          string `json:"os"`
	OSVersion   string `json:"os_version"`
	Hostname    string `json:"hostname"`
	CPUModel    string `json:"cpu_model"`
	CPUCores    int    `json:"cpu_cores"`
	GPUModel    string `json:"gpu_model"`
	TotalMemory uint64 `json:"total_memory"`
	Disks       []Disk `json:"disks"`
}

// Metrics 是实时资源快照。CPUUsagePercent 与网络速率来自相邻两次采样的
// 差值；首次采样时它们为零，内存与磁盘立即有值。
type Metrics struct {
	CPUUsagePercent float64 `json:"cpu_usage_percent"`
	MemoryUsed      uint64  `json:"memory_used"`
	MemoryTotal     uint64  `json:"memory_total"`
	DiskUsed        uint64  `json:"disk_used"`
	DiskTotal       uint64  `json:"disk_total"`
	NetworkRXBPS    uint64  `json:"network_rx_bps"`
	NetworkTXBPS    uint64  `json:"network_tx_bps"`
}

// sample 是单次原始计数，用于相邻两次之间求差。
type sample struct {
	at       time.Time
	cpuTotal uint64
	cpuIdle  uint64
	rxBytes  uint64
	txBytes  uint64
}

// Sampler 保存上一次原始计数。它不是并发安全的单点缓存而是状态机：
// 并发调用在锁内串行，后一次自然成为前一次的「下一次采样」。
type Sampler struct {
	mu   sync.Mutex
	last *sample
	// firstWait 是首次采样为拿到非零速率而等待的窗口；测试里缩短它。
	firstWait time.Duration
}

func NewSampler() *Sampler {
	return &Sampler{firstWait: 200 * time.Millisecond}
}

// Sample 返回当前实时指标。ctx 取消时立即返回零值快照。
func (s *Sampler) Sample(ctx context.Context) Metrics {
	select {
	case <-ctx.Done():
		return Metrics{}
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cur := s.readRawCounters()
	// 内存与磁盘是瞬时值，不依赖差值，任何一次采样都直接带上。
	used, total, ok := readMemory()
	m := Metrics{}
	if ok {
		m.MemoryUsed, m.MemoryTotal = used, total
	}
	if dUsed, dTotal, dOK := readRootDisk(); dOK {
		m.DiskUsed, m.DiskTotal = dUsed, dTotal
	}

	if s.last == nil {
		// 首次采样没有差值可算；等一个短窗口再采一次，让首个响应就带
		// 速率，避免客户端拿到全零后误显示「0 B/s」。
		if s.firstWait > 0 {
			timer := time.NewTimer(s.firstWait)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				s.last = &cur
				return m
			case <-timer.C:
			}
			cur = s.readRawCounters()
		}
		s.last = &cur
		return m
	}

	elapsed := cur.at.Sub(s.last.at).Seconds()
	if elapsed > 0 {
		if totalDelta := cur.cpuTotal - s.last.cpuTotal; totalDelta > 0 && cur.cpuTotal >= s.last.cpuTotal {
			idleDelta := cur.cpuIdle - s.last.cpuIdle
			if idleDelta <= totalDelta {
				m.CPUUsagePercent = (1 - float64(idleDelta)/float64(totalDelta)) * 100
			}
		}
		// 计数回绕（32 位计数器）时差值变负，跳过速率，保留其余字段。
		if cur.rxBytes >= s.last.rxBytes && cur.txBytes >= s.last.txBytes {
			m.NetworkRXBPS = uint64(float64(cur.rxBytes-s.last.rxBytes) / elapsed)
			m.NetworkTXBPS = uint64(float64(cur.txBytes-s.last.txBytes) / elapsed)
		}
	}
	s.last = &cur
	return m
}

func (s *Sampler) readRawCounters() sample {
	cur := sample{at: time.Now()}
	if total, idle, ok := readCPUCounters(); ok {
		cur.cpuTotal, cur.cpuIdle = total, idle
	}
	if rx, tx, ok := readNetCounters(); ok {
		cur.rxBytes, cur.txBytes = rx, tx
	}
	return cur
}

var (
	hardwareOnce sync.Once
	hardwareVal  Hardware
)

// CachedHardware 返回进程内缓存的静态硬件画像。字段缺失不影响其余字段。
func CachedHardware() Hardware {
	hardwareOnce.Do(func() {
		hardwareVal = collectHardware()
	})
	return hardwareVal
}
