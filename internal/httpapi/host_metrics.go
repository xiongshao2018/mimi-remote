package httpapi

import (
	"net/http"

	"github.com/gaixianggeng/mimi-remote/internal/hostmetrics"
)

// hostHardwareHandler 返回电脑静态硬件画像（显卡/内存/硬盘/CPU/系统）。
// 进程内只采一次，响应很小，供 iOS 设备页在电脑行下方展开摘要。
func (r *Router) hostHardwareHandler(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, hostmetrics.CachedHardware())
}

// hostMetricsHandler 返回实时资源快照（CPU/内存/磁盘用量、网络速率）。
// CPU 占用与速率来自相邻两次采样的差值，iOS 输入框指标条按固定间隔轮询；
// 每次 GET 都推进采样窗口，因此轮询间隔即速率窗口。
func (r *Router) hostMetricsHandler(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, r.sampler().Sample(req.Context()))
}

func (r *Router) sampler() *hostmetrics.Sampler {
	r.hostSamplerOnce.Do(func() {
		if r.hostSampler == nil {
			r.hostSampler = hostmetrics.NewSampler()
		}
	})
	return r.hostSampler
}
