package hub

import (
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"sitedeck/internal/access"
	"sitedeck/internal/identity"
	"sitedeck/internal/protocol"
)

//go:embed web/*
var adminFiles embed.FS

const adminCookie = "sitedeck_admin"

type adminSession struct {
	CSRF    string
	Expires time.Time
}
type loginAttempt struct {
	Count int
	Reset time.Time
}
type DeviceView struct {
	Device
	Online      bool `json:"online"`
	Connections int  `json:"connections"`
}
type ChannelView struct {
	ID              string     `json:"id"`
	Device          string     `json:"deviceId"`
	DeviceName      string     `json:"deviceName"`
	Grant           string     `json:"grantId,omitempty"`
	Transport       string     `json:"transport"`
	CreatedAt       time.Time  `json:"createdAt"`
	AuthenticatedAt *time.Time `json:"authenticatedAt,omitempty"`
	LastActivity    time.Time  `json:"lastActivity"`
	ExpiresAt       *time.Time `json:"expiresAt,omitempty"`
	RelayBytes      uint64     `json:"relayBytes"`
	RemoteAddress   string     `json:"remoteAddress"`
}

func (h *Hub) adminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/admin/", http.StatusFound)
	})
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/admin/", h.adminHTTP)
}
func (h *Hub) adminHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	if h.admin == "" {
		http.Error(w, "Hub management is not enabled", http.StatusServiceUnavailable)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/admin/api/") {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", 405)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/admin/")
		contentType := "text/html; charset=utf-8"
		switch name {
		case "":
			name = "index.html"
		case "app.js":
			contentType = "text/javascript; charset=utf-8"
		case "style.css":
			contentType = "text/css; charset=utf-8"
		default:
			http.NotFound(w, r)
			return
		}
		b, err := adminFiles.ReadFile("web/" + name)
		if err != nil {
			http.Error(w, "management asset unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", contentType)
		if r.Method != http.MethodHead {
			_, _ = w.Write(b)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if !sameOrigin(r) {
		adminError(w, 403, "ORIGIN_DENIED", "请求来源不匹配")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/admin/api/")
	if path == "login" && r.Method == http.MethodPost {
		h.login(w, r)
		return
	}
	cookie, err := r.Cookie(adminCookie)
	if err != nil {
		adminError(w, 401, "AUTH_REQUIRED", "请先登录管理后台")
		return
	}
	h.mu.Lock()
	session, exists := h.adminSessions[cookie.Value]
	if exists && !time.Now().Before(session.Expires) {
		delete(h.adminSessions, cookie.Value)
		exists = false
	}
	h.mu.Unlock()
	if !exists {
		adminError(w, 401, "AUTH_REQUIRED", "管理会话已过期，请重新登录")
		return
	}
	if r.Method != http.MethodGet && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRF)) != 1 {
		adminError(w, 403, "CSRF_DENIED", "请求验证失败，请刷新页面")
		return
	}
	switch {
	case path == "session" && r.Method == http.MethodGet:
		adminJSON(w, 200, map[string]any{"authenticated": true, "csrfToken": session.CSRF, "expiresAt": session.Expires, "version": protocol.Version})
	case path == "logout" && r.Method == http.MethodPost:
		h.mu.Lock()
		delete(h.adminSessions, cookie.Value)
		h.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: adminCookie, Path: "/admin", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		adminJSON(w, 200, map[string]bool{"loggedOut": true})
	case path == "overview" && r.Method == http.MethodGet:
		adminJSON(w, 200, h.overview())
	case strings.HasPrefix(path, "devices/") && r.Method == http.MethodPatch:
		h.updateDevice(w, r, strings.TrimPrefix(path, "devices/"))
	case path == "grants" && r.Method == http.MethodPost:
		h.createGrant(w, r)
	case strings.HasPrefix(path, "grants/") && r.Method == http.MethodDelete:
		h.revokeGrant(w, r, strings.TrimPrefix(path, "grants/"))
	case strings.HasPrefix(path, "connections/") && r.Method == http.MethodDelete:
		h.disconnect(w, r, strings.TrimPrefix(path, "connections/"))
	default:
		adminError(w, 404, "NOT_FOUND", "接口不存在或方法不支持")
	}
}
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && strings.EqualFold(u.Host, r.Host) && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
func decodeAdmin(w http.ResponseWriter, r *http.Request, out any) bool {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		adminError(w, 415, "INVALID_CONTENT_TYPE", "请使用 JSON 请求")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		adminError(w, 400, "INVALID_INPUT", "请求内容无效")
		return false
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		adminError(w, 400, "INVALID_INPUT", "请求必须只有一个 JSON 对象")
		return false
	}
	return true
}
func (h *Hub) login(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	now := time.Now().UTC()
	h.mu.Lock()
	attempt := h.loginAttempts[host]
	if !now.Before(attempt.Reset) {
		attempt = loginAttempt{Reset: now.Add(time.Minute)}
	}
	if attempt.Count >= 10 {
		h.mu.Unlock()
		adminError(w, 429, "RATE_LIMITED", "登录尝试过多，请一分钟后重试")
		return
	}
	if _, known := h.loginAttempts[host]; !known && len(h.loginAttempts) >= 1024 {
		for key, item := range h.loginAttempts {
			if !now.Before(item.Reset) {
				delete(h.loginAttempts, key)
			}
		}
		if len(h.loginAttempts) >= 1024 {
			h.mu.Unlock()
			adminError(w, 429, "RATE_LIMITED", "登录请求过多，请稍后重试")
			return
		}
	}
	attempt.Count++
	h.loginAttempts[host] = attempt
	h.mu.Unlock()
	var body struct {
		Secret string `json:"secret"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(body.Secret)), []byte(h.admin)) != 1 {
		adminError(w, 401, "AUTH_FAILED", "管理密钥不正确")
		return
	}
	id := identity.Random(32)
	session := adminSession{CSRF: identity.Random(32), Expires: now.Add(8 * time.Hour)}
	h.mu.Lock()
	for key, item := range h.adminSessions {
		if !now.Before(item.Expires) {
			delete(h.adminSessions, key)
		}
	}
	if len(h.adminSessions) >= 64 {
		h.mu.Unlock()
		adminError(w, 429, "SESSION_LIMIT", "管理会话数量已达上限")
		return
	}
	h.adminSessions[id] = session
	delete(h.loginAttempts, host)
	h.mu.Unlock()
	secure := r.TLS != nil || strings.HasPrefix(r.Header.Get("Origin"), "https://")
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: id, Path: "/admin", MaxAge: 8 * 60 * 60, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode})
	adminJSON(w, 200, map[string]any{"authenticated": true, "csrfToken": session.CSRF, "expiresAt": session.Expires, "version": protocol.Version})
}
func (h *Hub) overview() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	devices := make([]DeviceView, 0, len(h.state.Devices))
	grants := make([]Grant, 0, len(h.state.Grants))
	channels := make([]ChannelView, 0, len(h.channels))
	counts := map[string]int{}
	for _, ch := range h.channels {
		counts[ch.agent.device]++
		channels = append(channels, h.channelViewLocked(ch))
	}
	for _, d := range h.state.Devices {
		devices = append(devices, DeviceView{Device: d, Online: h.agents[d.ID] != nil, Connections: counts[d.ID]})
	}
	for _, g := range h.state.Grants {
		grants = append(grants, g.Grant)
	}
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].Online != devices[j].Online {
			return devices[i].Online
		}
		return devices[i].Name < devices[j].Name
	})
	sort.Slice(grants, func(i, j int) bool { return grants[i].CreatedAt.After(grants[j].CreatedAt) })
	sort.Slice(channels, func(i, j int) bool { return channels[i].CreatedAt.After(channels[j].CreatedAt) })
	return map[string]any{"devices": devices, "grants": grants, "connections": channels, "asOf": time.Now().UTC(), "version": protocol.Version, "maxChannelsPerDevice": h.MaxChannels}
}
func (h *Hub) channelViewLocked(ch *channel) ChannelView {
	return ChannelView{ID: ch.id, Device: ch.agent.device, DeviceName: h.state.Devices[ch.agent.device].Name, Grant: ch.grant, Transport: ch.transport, CreatedAt: ch.createdAt, AuthenticatedAt: ch.authenticatedAt, LastActivity: ch.lastActivity, ExpiresAt: ch.expiresAt, RelayBytes: ch.bytes, RemoteAddress: ch.remoteAddress}
}
func (h *Hub) updateDevice(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Name        *string `json:"name"`
		Blocked     *bool   `json:"blocked"`
		AllowPaired *bool   `json:"allowPaired"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	if body.Name == nil && body.Blocked == nil && body.AllowPaired == nil {
		adminError(w, 400, "INVALID_INPUT", "请提供需要修改的设备属性")
		return
	}
	if body.Name != nil {
		trimmed := strings.TrimSpace(*body.Name)
		body.Name = &trimmed
		if !validName(trimmed) {
			adminError(w, 400, "INVALID_NAME", "设备名称须为 1–80 个字符，且不含控制字符")
			return
		}
	}
	h.mu.Lock()
	d, exists := h.state.Devices[id]
	if !exists {
		h.mu.Unlock()
		adminError(w, 404, "DEVICE_NOT_FOUND", "设备不存在")
		return
	}
	if body.Name != nil {
		d.Name = *body.Name
	}
	if body.Blocked != nil {
		d.Blocked = *body.Blocked
	}
	if body.AllowPaired != nil {
		d.AllowPaired = *body.AllowPaired
	}
	next := h.state.clone()
	next.Devices[id] = d
	if err := h.commitLocked(next); err != nil {
		h.mu.Unlock()
		storageError(w, err)
		return
	}
	var closeList []*channel
	for _, ch := range h.channels {
		if ch.agent.device == id && (d.Blocked || (!d.AllowPaired && ch.grant == "")) {
			closeList = append(closeList, ch)
		}
	}
	h.mu.Unlock()
	for _, ch := range closeList {
		h.closeChannel(ch, "ACCESS_POLICY_CHANGED")
	}
	log.Printf("admin updated device=%q name=%q blocked=%t allowPaired=%t", id, d.Name, d.Blocked, d.AllowPaired)
	adminJSON(w, 200, map[string]any{"device": d, "disconnected": len(closeList)})
}
func (h *Hub) createGrant(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Device       string    `json:"deviceId"`
		Name         string    `json:"name"`
		Scopes       []string  `json:"scopes"`
		ExpiresAt    time.Time `json:"expiresAt"`
		RequireGrant *bool     `json:"requireGrant"`
	}
	if !decodeAdmin(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if !validName(body.Name) || !access.ValidScopes(body.Scopes) {
		adminError(w, 400, "INVALID_INPUT", "请填写授权名称，并至少选择一项有效权限")
		return
	}
	now := time.Now().UTC()
	if body.ExpiresAt.IsZero() {
		body.ExpiresAt = now.Add(8 * time.Hour)
	}
	body.ExpiresAt = body.ExpiresAt.UTC().Truncate(time.Millisecond)
	if !body.ExpiresAt.After(now) || body.ExpiresAt.After(now.Add(30*24*time.Hour)) {
		adminError(w, 400, "INVALID_EXPIRY", "有效期须在未来 30 天内")
		return
	}
	token := "oa_" + identity.Random(32)
	hash := sha256.Sum256([]byte(token))
	grant := Grant{ID: identity.Random(16), Device: body.Device, Name: body.Name, Scopes: body.Scopes, CreatedAt: now, ExpiresAt: body.ExpiresAt.UTC()}
	h.mu.Lock()
	device, exists := h.state.Devices[body.Device]
	if !exists {
		h.mu.Unlock()
		adminError(w, 404, "DEVICE_NOT_FOUND", "设备不存在")
		return
	}
	if device.AccessProtocol != 1 {
		h.mu.Unlock()
		adminError(w, 409, "AGENT_UPGRADE_REQUIRED", "现场 Agent 需升级到支持 Hub 授权的版本")
		return
	}
	if len(h.state.Grants) >= 4096 {
		h.mu.Unlock()
		adminError(w, 409, "GRANT_LIMIT", "授权记录数量已达上限")
		return
	}
	next := h.state.clone()
	next.Grants[grant.ID] = storedGrant{Grant: grant, Hash: hex.EncodeToString(hash[:])}
	requireGrant := body.RequireGrant == nil || *body.RequireGrant
	if requireGrant {
		device.AllowPaired = false
		next.Devices[device.ID] = device
	}
	if err := h.commitLocked(next); err != nil {
		h.mu.Unlock()
		storageError(w, err)
		return
	}
	var closeList []*channel
	if requireGrant {
		for _, ch := range h.channels {
			if ch.agent.device == device.ID && ch.grant == "" {
				closeList = append(closeList, ch)
			}
		}
	}
	h.mu.Unlock()
	for _, ch := range closeList {
		h.closeChannel(ch, "ACCESS_POLICY_CHANGED")
	}
	log.Printf("admin created access grant=%s device=%q", grant.ID, grant.Device)
	adminJSON(w, 201, map[string]any{"grant": grant, "accessToken": token, "requireGrant": requireGrant, "disconnected": len(closeList)})
}
func (h *Hub) revokeGrant(w http.ResponseWriter, r *http.Request, id string) {
	h.mu.Lock()
	g, exists := h.state.Grants[id]
	if !exists {
		h.mu.Unlock()
		adminError(w, 404, "GRANT_NOT_FOUND", "授权不存在")
		return
	}
	if g.RevokedAt == nil {
		now := time.Now().UTC()
		g.RevokedAt = &now
		next := h.state.clone()
		next.Grants[id] = g
		if err := h.commitLocked(next); err != nil {
			h.mu.Unlock()
			storageError(w, err)
			return
		}
	}
	var list []*channel
	for _, ch := range h.channels {
		if ch.grant == id {
			list = append(list, ch)
		}
	}
	h.mu.Unlock()
	for _, ch := range list {
		h.closeChannel(ch, "ACCESS_REVOKED")
	}
	log.Printf("admin revoked access grant=%s", id)
	adminJSON(w, 200, map[string]any{"revoked": true, "disconnected": len(list)})
}
func (h *Hub) disconnect(w http.ResponseWriter, r *http.Request, id string) {
	h.mu.Lock()
	ch := h.channels[id]
	h.mu.Unlock()
	if ch != nil {
		h.closeChannel(ch, "ADMIN_DISCONNECTED")
	}
	adminJSON(w, 200, map[string]bool{"closed": true})
}
func adminJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func adminError(w http.ResponseWriter, status int, code, message string) {
	adminJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func storageError(w http.ResponseWriter, err error) {
	log.Printf("Hub state write failed: %v", err)
	adminError(w, 500, "STORAGE_ERROR", "保存失败，原设置未修改，请检查状态文件目录")
}
