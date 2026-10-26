// Package strata 从可选的外部推理引擎（Strata）拉取监控快照。
// 它是纯读取的旁路监控：连不上、没配置或返回结构不认识时全部返回
// "unavailable"，不影响 agentd 自身功能与硬件指标。
package strata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Snapshot 是 iOS 设备页监控卡片里的「推理引擎」部分。字段全部可选：
// Strata 版本差异时缺失字段留 nil，客户端按缺省隐藏对应行。
type Snapshot struct {
	Available bool `json:"available"`

	// 引擎状态（live）
	State          string   `json:"state"`
	Phase          string   `json:"phase,omitempty"`
	Queued         *int     `json:"queued,omitempty"`
	PromptTokens   *int64   `json:"prompt_tokens,omitempty"`
	PromptRead     *int64   `json:"prompt_read,omitempty"`
	Generated      *int64   `json:"generated,omitempty"`
	ElapsedSeconds *float64 `json:"elapsed_s,omitempty"`
	TokPerSecond   *float64 `json:"tok_s,omitempty"`
	PrefillTokS    *float64 `json:"prefill_tok_s_mean,omitempty"`

	// 引擎事实（engine）
	Model             string `json:"model,omitempty"`
	Version           string `json:"version,omitempty"`
	MaxContext        *int64 `json:"max_context,omitempty"`
	ContextFill       *int64 `json:"context,omitempty"`
	ExpertSlots       *int   `json:"expert_slots,omitempty"`
	ExpertCacheMiB    *int   `json:"expert_cache_mib,omitempty"`
	KVResident        *int64 `json:"kv_resident,omitempty"`
	VRAMFreeMiB       *int   `json:"vram_free_mib,omitempty"`
	ConversationSlots *int   `json:"conversation_cache_slots,omitempty"`
}

// Fetcher 持有 Strata 的地址与凭据。零值 Fetcher 直接报不可用。
type Fetcher struct {
	baseURL string
	apiKey  string
	timeout time.Duration
	client  *http.Client
}

func NewFetcher(baseURL, apiKey string, timeout time.Duration) *Fetcher {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &Fetcher{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		timeout: timeout,
		client:  &http.Client{Timeout: timeout},
	}
}

// Enabled 报告是否配置了可用的 Strata 地址。
func (f *Fetcher) Enabled() bool { return f != nil && f.baseURL != "" }

// ErrUnavailable 是「没有配置 Strata」的统一错误，调用方据此整块隐藏。
var ErrUnavailable = errors.New("strata: not configured")

// Fetch 拉一次 /metrics 并裁剪成 Snapshot。ctx 取消、超时或响应不可识别
// 都返回 Snapshot{Available: false} 加错误，不重试。
func (f *Fetcher) Fetch(ctx context.Context) (Snapshot, error) {
	if !f.Enabled() {
		return Snapshot{}, ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.baseURL+"/metrics", nil)
	if err != nil {
		return Snapshot{}, err
	}
	if f.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+f.apiKey)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return Snapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Snapshot{}, fmt.Errorf("strata: unexpected status %d", resp.StatusCode)
	}
	// 只解码关心的字段；Strata 的 /metrics 字段很多，结构演进不影响这里。
	// 只解码关心的字段：live 与 engine 都按原始消息保留，Strata 的字段
	// 演进（新增/改名/类型变化）不会影响这里解码成功。
	var payload struct {
		Live   map[string]json.RawMessage `json:"live"`
		Engine map[string]json.RawMessage `json:"engine"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Snapshot{}, err
	}
	if len(payload.Live) == 0 && len(payload.Engine) == 0 {
		return Snapshot{}, errors.New("strata: empty metrics payload")
	}

	s := Snapshot{Available: true}
	str := func(m map[string]json.RawMessage, key string) string {
		var v string
		if raw, ok := m[key]; ok && json.Unmarshal(raw, &v) == nil {
			return v
		}
		return ""
	}
	num := func(m map[string]json.RawMessage, key string) *float64 {
		raw, ok := m[key]
		if !ok || string(raw) == "null" {
			return nil
		}
		var v float64
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil
		}
		return &v
	}
	intOf := func(m map[string]json.RawMessage, key string) *int {
		if v := num(m, key); v != nil {
			i := int(*v)
			return &i
		}
		return nil
	}
	int64Of := func(m map[string]json.RawMessage, key string) *int64 {
		if v := num(m, key); v != nil {
			i := int64(*v)
			return &i
		}
		return nil
	}

	s.State = str(payload.Live, "state")
	s.Phase = str(payload.Live, "phase")
	s.Queued = intOf(payload.Live, "queued")
	s.PromptTokens = int64Of(payload.Live, "prompt_tokens")
	s.PromptRead = int64Of(payload.Live, "prompt_read")
	s.Generated = int64Of(payload.Live, "generated")
	s.ElapsedSeconds = num(payload.Live, "elapsed_s")
	s.TokPerSecond = num(payload.Live, "tok_s")
	s.PrefillTokS = num(payload.Live, "prefill_tok_s_mean")

	s.Model = str(payload.Engine, "model")
	s.Version = str(payload.Engine, "version")
	s.MaxContext = int64Of(payload.Engine, "max_context")
	s.ContextFill = int64Of(payload.Engine, "context")
	s.ExpertSlots = intOf(payload.Engine, "expert_slots")
	s.ExpertCacheMiB = intOf(payload.Engine, "expert_cache_mib")
	s.KVResident = int64Of(payload.Engine, "kv_resident")
	s.VRAMFreeMiB = intOf(payload.Engine, "vram_free_mib")
	s.ConversationSlots = intOf(payload.Engine, "conversation_cache_slots")
	return s, nil
}
