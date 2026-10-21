package httpapi

import (
	"net/http"
	"strings"

	"github.com/gaixianggeng/mimi-remote/internal/config"
	"github.com/gaixianggeng/mimi-remote/internal/networkaccess"
	agentsetup "github.com/gaixianggeng/mimi-remote/internal/setup"
)

func (r *Router) moduleStatusHandler(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !runtimeStatusLoopbackRequest(req) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	tailscale, lan := networkaccess.Availability()
	public := agentsetup.PublicAccessRuntimeStatus(r.configPath, r.cfg)
	fingerprint := strings.TrimSpace(r.tlsFingerprint)
	if fingerprint == "" {
		fingerprint = public.Fingerprint
	}
	writeJSON(w, http.StatusOK, config.ModuleStatus{
		CodexEnabled: r.cfg.Codex.IsEnabled(), ClaudeEnabled: r.cfg.Claude.Enabled,
		TailscaleEnabled: r.cfg.TailscaleAccessEnabled(), LANEnabled: r.cfg.LANAccessEnabled(),
		TailscaleAvailable: tailscale, LANAvailable: lan,
		PublicEnabled: public.Enabled, PublicAvailable: public.Endpoint != "",
		PublicEndpoint: public.Endpoint, PublicFingerprint: fingerprint,
	})
}
