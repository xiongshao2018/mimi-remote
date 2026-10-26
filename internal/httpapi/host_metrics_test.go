package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/gaixianggeng/mimi-remote/internal/hostmetrics"
)

func TestHostHardwareEndpoint(t *testing.T) {
	server := newTestServer(t)
	rec := httptest.NewRecorder()
	server.handler.ServeHTTP(rec, authedRequest(t, http.MethodGet, "/api/host/hardware", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var hw hostmetrics.Hardware
	if err := json.Unmarshal(rec.Body.Bytes(), &hw); err != nil {
		t.Fatal(err)
	}
	if hw.OS != runtime.GOOS {
		t.Fatalf("os = %q, want %q", hw.OS, runtime.GOOS)
	}
	if hw.Hostname == "" {
		t.Fatal("hostname is empty")
	}
	if hw.TotalMemory == 0 {
		t.Fatal("total_memory is zero")
	}
}

func TestHostMetricsEndpoint(t *testing.T) {
	server := newTestServer(t)
	rec := httptest.NewRecorder()
	server.handler.ServeHTTP(rec, authedRequest(t, http.MethodGet, "/api/host/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var m hostmetrics.Metrics
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.MemoryTotal == 0 {
		t.Fatal("memory_total is zero")
	}
	if m.DiskTotal == 0 {
		t.Fatal("disk_total is zero")
	}
}

func TestHostMetricsEndpointRejectsPost(t *testing.T) {
	server := newTestServer(t)
	rec := httptest.NewRecorder()
	server.handler.ServeHTTP(rec, authedRequest(t, http.MethodPost, "/api/host/metrics", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestHostSamplerComputesUsageFromAdjacentSamples(t *testing.T) {
	sampler := hostmetrics.NewSampler()
	first := sampler.Sample(t.Context())
	if first.MemoryTotal == 0 {
		t.Fatal("first sample must carry memory even without a diff window")
	}
	second := sampler.Sample(t.Context())
	// 第二次采样必须能算出 CPU 占用（0~100 开区间外的 0 也允许：完全空闲的
	// CI 机器上 idle 差值可能等于 total 差值）。
	if second.CPUUsagePercent < 0 || second.CPUUsagePercent > 100 {
		t.Fatalf("cpu usage = %f, want within [0,100]", second.CPUUsagePercent)
	}
}
