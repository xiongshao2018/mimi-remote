package httpapi

import (
	"net/http"

	"github.com/gaixianggeng/mimi-remote/internal/config"
	"github.com/gaixianggeng/mimi-remote/internal/hostmetrics"
	"github.com/gaixianggeng/mimi-remote/internal/strata"
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

// hostStrataHandler 返回可选外部推理引擎（Strata）的监控快照。没配置或
// 拉不到时返回 available=false（HTTP 200），客户端整块隐藏推理部分；
// 只有「未配置」这一种情况才用 404 让客户端本轮不再重试。
func (r *Router) hostStrataHandler(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	fetcher := r.strataFetcher()
	if !fetcher.Enabled() {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error":     "strata not configured",
			"available": false,
		})
		return
	}
	snapshot, err := fetcher.Fetch(req.Context())
	if err != nil {
		// 连不上是临时状态：按 200 + available=false 返回，保留下轮自动恢复。
		writeJSON(w, http.StatusOK, strata.Snapshot{Available: false})
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (r *Router) strataFetcher() *strata.Fetcher {
	r.strataOnce.Do(func() {
		if r.strata == nil {
			if cfg := r.configStrata(); cfg != nil {
				r.strata = strata.NewFetcher(cfg.BaseURL, cfg.APIKey, cfg.TimeoutOrDefault())
			} else {
				// 未配置时也建一个空 Fetcher，避免每次请求都进 once 判断。
				r.strata = strata.NewFetcher("", "", 0)
			}
		}
	})
	return r.strata
}

// configStrata 返回配置里的 Strata 监控源。配置是启动快照，进程内只在
// 首次请求时读一次；未配置返回 nil。
func (r *Router) configStrata() *config.StrataConfig {
	return r.cfg.Strata
}

func (r *Router) sampler() *hostmetrics.Sampler {
	r.hostSamplerOnce.Do(func() {
		if r.hostSampler == nil {
			r.hostSampler = hostmetrics.NewSampler()
		}
	})
	return r.hostSampler
}
