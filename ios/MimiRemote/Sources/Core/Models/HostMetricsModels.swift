import Foundation

/// agentd `/api/host/hardware` 的静态硬件画像。全部字段是纯加法契约：
/// 旧 agentd 没有该端点时客户端整块隐藏，字段缺失时按缺省处理。
struct HostHardwareResponse: Codable, Equatable {
    struct Disk: Codable, Equatable {
        let name: String
        let total: UInt64
        let free: UInt64

        enum CodingKeys: String, CodingKey {
            case name
            case total
            case free
        }
    }

    let os: String?
    let osVersion: String?
    let hostname: String?
    let cpuModel: String?
    let cpuCores: Int?
    let gpuModel: String?
    let totalMemory: UInt64?
    let disks: [Disk]?

    init(
        os: String? = nil,
        osVersion: String? = nil,
        hostname: String? = nil,
        cpuModel: String? = nil,
        cpuCores: Int? = nil,
        gpuModel: String? = nil,
        totalMemory: UInt64? = nil,
        disks: [Disk]? = nil
    ) {
        self.os = os
        self.osVersion = osVersion
        self.hostname = hostname
        self.cpuModel = cpuModel
        self.cpuCores = cpuCores
        self.gpuModel = gpuModel
        self.totalMemory = totalMemory
        self.disks = disks
    }

    enum CodingKeys: String, CodingKey {
        case os
        case osVersion = "os_version"
        case hostname
        case cpuModel = "cpu_model"
        case cpuCores = "cpu_cores"
        case gpuModel = "gpu_model"
        case totalMemory = "total_memory"
        case disks
    }

    /// 设备页电脑行下方的一行摘要：内存总量 + 磁盘总量 + 显卡型号。
    /// 任何字段缺失都跳过，全部缺失时返回 nil（整行不渲染）。
    var summaryLine: String? {
        var parts: [String] = []
        if let totalMemory, totalMemory > 0 {
            parts.append(ByteFormat.formattedMemory(totalMemory))
        }
        if let disks, !disks.isEmpty {
            let total = disks.map(\.total).reduce(0, &+)
            if total > 0 {
                parts.append(ByteFormat.formattedMemory(total))
            }
        }
        if let gpuModel, !gpuModel.isEmpty {
            parts.append(gpuModel)
        }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }
}

/// agentd `/api/host/metrics` 的实时资源快照。会话输入框指标条按固定
/// 间隔轮询；服务端按相邻两次采样求 CPU 与网络速率差值。
struct HostMetricsResponse: Codable, Equatable {
    let cpuUsagePercent: Double
    let memoryUsed: UInt64
    let memoryTotal: UInt64
    let diskUsed: UInt64
    let diskTotal: UInt64
    let networkRXBPS: UInt64
    let networkTXBPS: UInt64

    init(
        cpuUsagePercent: Double = 0,
        memoryUsed: UInt64 = 0,
        memoryTotal: UInt64 = 0,
        diskUsed: UInt64 = 0,
        diskTotal: UInt64 = 0,
        networkRXBPS: UInt64 = 0,
        networkTXBPS: UInt64 = 0
    ) {
        self.cpuUsagePercent = cpuUsagePercent
        self.memoryUsed = memoryUsed
        self.memoryTotal = memoryTotal
        self.diskUsed = diskUsed
        self.diskTotal = diskTotal
        self.networkRXBPS = networkRXBPS
        self.networkTXBPS = networkTXBPS
    }

    enum CodingKeys: String, CodingKey {
        case cpuUsagePercent = "cpu_usage_percent"
        case memoryUsed = "memory_used"
        case memoryTotal = "memory_total"
        case diskUsed = "disk_used"
        case diskTotal = "disk_total"
        case networkRXBPS = "network_rx_bps"
        case networkTXBPS = "network_tx_bps"
    }
}

/// 指标展示的统一格式化：设备页与输入框共用同一口径，避免两处四舍五入分叉。
enum ByteFormat {
    static func formattedMemory(_ bytes: UInt64) -> String {
        let mib = Double(bytes) / (1024 * 1024)
        if mib < 1024 {
            return "\(Int(mib.rounded())) MB"
        }
        let gib = mib / 1024
        if gib >= 1024 {
            // 磁盘厂商标称用十进制 TB；文件系统容量约为其 0.91 倍 TiB，
            // 按十进制换算才能显示回「2 TB」这类标称值。
            let tb = Double(bytes) / 1_000_000_000_000
            return String(format: tb < 10 ? "%.1f TB" : "%.0f TB", tb)
        }
        // 内存标称值是 2 的幂，系统读到的可用量因内核保留约低 4~5%；
        // 5% 内直接报标称值，避免「61.4 GB」读起来像缺容量。
        let nominal = pow(2.0, log2(gib).rounded())
        if abs(gib - nominal) / nominal < 0.05 {
            return "\(Int(nominal)) GB"
        }
        return String(format: "%.1f GB", gib)
    }

    static func formattedRate(_ bytesPerSecond: UInt64) -> String {
        if bytesPerSecond < 1024 {
            return "\(bytesPerSecond) B/s"
        }
        let kib = Double(bytesPerSecond) / 1024
        if kib < 1024 {
            return String(format: "%.0f KB/s", kib)
        }
        let mib = kib / 1024
        if mib < 10 {
            return String(format: "%.1f MB/s", mib)
        }
        return String(format: "%.0f MB/s", mib)
    }

    static func formattedPercent(_ percent: Double) -> String {
        "\(Int(percent.rounded()))%"
    }
}
