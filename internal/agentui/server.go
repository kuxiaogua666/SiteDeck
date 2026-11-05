package agentui

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"sitedeck/internal/identity"
	"sitedeck/internal/protocol"
	"sitedeck/internal/serialport"
)

//go:embed web/* web/vendor/*
var files embed.FS

type Descriptor struct {
	URL       string `json:"url"`
	PID       int    `json:"pid"`
	Secret    string `json:"secret"`
	NativePID int    `json:"nativePid,omitempty"`
}
type browserSession struct {
	CSRF string
	Seen time.Time
}
type launchToken struct {
	File    string
	Expires time.Time
}
type Server struct {
	Controller    *Controller
	URL           string
	ctx           context.Context
	cancel        context.CancelFunc
	listener      net.Listener
	http          *http.Server
	mu            sync.Mutex
	descriptor    Descriptor
	sessions      map[string]browserSession
	tokens        map[string]launchToken
	loginFailures int
	loginReset    time.Time
	done          chan struct{}
	network       *networkManager
}

func Listen(ctx context.Context, c *Controller, address string) (*Server, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("Agent 图形界面只能监听回环 IP")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("Agent 界面端口无法使用：%w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{Controller: c, URL: "http://" + listener.Addr().String(), ctx: ctx, cancel: cancel, listener: listener, sessions: map[string]browserSession{}, tokens: map[string]launchToken{}, done: make(chan struct{})}
	s.network = newNetworkManager()
	s.descriptor = Descriptor{URL: s.URL, PID: os.Getpid(), Secret: identity.Random(32)}
	if err = s.writeDescriptor(); err != nil {
		listener.Close()
		cancel()
		return nil, err
	}
	if err = writePrivate(filepath.Join(c.Directory, "ui-access.key"), []byte(s.descriptor.Secret+"\n")); err != nil {
		listener.Close()
		cancel()
		os.Remove(filepath.Join(c.Directory, "ui-runtime.json"))
		return nil, err
	}
	s.http = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	go func() { defer close(s.done); _ = s.http.Serve(listener); cancel() }()
	go func() {
		<-ctx.Done()
		s.network.close()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.http.Shutdown(shutdown)
	}()
	go s.reap()
	return s, nil
}
func (s *Server) Context() context.Context { return s.ctx }
func (s *Server) writeDescriptor() error {
	b, err := json.Marshal(s.descriptor)
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(s.Controller.Directory, "ui-runtime.json"), b)
}
func (s *Server) SetNativePID(pid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.descriptor.NativePID = pid
	_ = s.writeDescriptor()
}
func (s *Server) Close() error {
	s.cancel()
	_ = s.Controller.Close()
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, item := range s.tokens {
		_ = os.Remove(item.File)
		delete(s.tokens, token)
	}
	path := filepath.Join(s.Controller.Directory, "ui-runtime.json")
	b, err := os.ReadFile(path)
	if err == nil {
		var current Descriptor
		if json.Unmarshal(b, &current) == nil && current.Secret == s.descriptor.Secret {
			_ = os.Remove(path)
		}
	}
	_ = os.Remove(filepath.Join(s.Controller.Directory, "ui-access.key"))
	return nil
}
func (s *Server) NewLaunchFile() (string, error) {
	token := identity.Random(32)
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, item := range s.tokens {
		if time.Now().After(item.Expires) {
			os.Remove(item.File)
			delete(s.tokens, key)
		}
	}
	if len(s.tokens) >= 16 {
		return "", fmt.Errorf("已有过多待打开的界面，请稍后重试")
	}
	script := `document.getElementById('launch').submit();`
	digest := sha256.Sum256([]byte(script))
	content := `<!doctype html><html><head><meta charset="utf-8"><meta name="referrer" content="no-referrer"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'sha256-` + base64.StdEncoding.EncodeToString(digest[:]) + `'; form-action ` + template.HTMLEscapeString(s.URL) + `"></head><body><p>正在打开 SiteDeck…</p><form id="launch" action="` + template.HTMLEscapeString(s.URL) + `/open" method="post"><input type="hidden" name="token" value="` + token + `"><noscript><button type="submit">打开现场终端</button></noscript></form><script>` + script + `</script></body></html>`
	path := filepath.Join(s.Controller.Directory, ".open-"+identity.Random(8)+".html")
	if err := writePrivate(path, []byte(content)); err != nil {
		return "", err
	}
	s.tokens[token] = launchToken{File: path, Expires: time.Now().Add(2 * time.Minute)}
	return path, nil
}
func ReadDescriptor(dir string) (Descriptor, error) {
	b, err := os.ReadFile(filepath.Join(dir, "ui-runtime.json"))
	if err != nil {
		return Descriptor{}, err
	}
	if len(b) > 8192 {
		return Descriptor{}, fmt.Errorf("invalid UI runtime descriptor")
	}
	var d Descriptor
	if err = json.Unmarshal(b, &d); err != nil {
		return d, err
	}
	u, err := url.Parse(d.URL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || len(d.Secret) != 64 {
		return d, fmt.Errorf("invalid UI runtime descriptor")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return d, fmt.Errorf("UI runtime must be local")
	}
	return d, nil
}
func Reopen(d Descriptor) (string, error) {
	req, err := http.NewRequest("POST", d.URL+"/control/open", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+d.Secret)
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return "", fmt.Errorf("已有界面进程不可访问")
	}
	var result struct {
		File string `json:"file"`
	}
	err = json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&result)
	return result.File, err
}
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	if r.Host != s.listener.Addr().String() {
		uiError(w, 403, "INVALID_HOST", "界面访问地址不匹配")
		return
	}
	if r.URL.Path == "/open" && r.Method == "POST" {
		s.open(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/control/") {
		s.mu.Lock()
		secret := s.descriptor.Secret
		s.mu.Unlock()
		if r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+secret)) != 1 {
			uiError(w, 403, "DENIED", "仅允许本机界面进程访问")
			return
		}
		if r.URL.Path == "/control/open" && r.Method == "POST" {
			path, err := s.NewLaunchFile()
			if err != nil {
				uiError(w, 409, "BUSY", err.Error())
				return
			}
			uiJSON(w, 200, map[string]string{"file": path})
			return
		}
		uiError(w, 404, "NOT_FOUND", "接口不存在")
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		s.asset(w, r)
		return
	}
	if !uiSameOrigin(r) {
		uiError(w, 403, "ORIGIN_DENIED", "请求来源不匹配")
		return
	}
	if r.URL.Path == "/api/login" && r.Method == "POST" {
		s.login(w, r)
		return
	}
	credential := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	session, valid := s.sessions[credential]
	if valid {
		session.Seen = time.Now()
		s.sessions[credential] = session
	}
	s.mu.Unlock()
	if !valid {
		uiError(w, 401, "AUTH_REQUIRED", "请通过现场程序打开界面，或选择本机 ui-access.key")
		return
	}
	if r.Method != "GET" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRF)) != 1 {
		uiError(w, 403, "CSRF_DENIED", "界面请求验证失败，请刷新后重试")
		return
	}
	member := "local-ui/" + credential
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/network/"):
		s.serveNetwork(w, r, member)
	case r.URL.Path == "/api/session" && r.Method == "GET":
		uiJSON(w, 200, map[string]any{"csrfToken": session.CSRF, "version": protocol.Version})
	case r.URL.Path == "/api/status" && r.Method == "GET":
		uiJSON(w, 200, s.Controller.Info())
	case r.URL.Path == "/api/config" && r.Method == "POST":
		var body struct {
			Settings Settings `json:"settings"`
			Restart  bool     `json:"restart"`
		}
		if !decodeUI(w, r, &body) {
			return
		}
		if err := s.Controller.Save(r.Context(), body.Settings, body.Restart); err != nil {
			uiError(w, 400, "CONFIG_ERROR", err.Error())
			return
		}
		uiJSON(w, 200, s.Controller.Info())
	case r.URL.Path == "/api/start" && r.Method == "POST":
		if err := s.Controller.Start(r.Context()); err != nil {
			uiError(w, 409, "START_FAILED", err.Error())
			return
		}
		uiJSON(w, 200, s.Controller.Info())
	case r.URL.Path == "/api/stop" && r.Method == "POST":
		if err := s.Controller.Stop(r.Context()); err != nil {
			uiError(w, 409, "STOP_FAILED", err.Error())
			return
		}
		uiJSON(w, 200, s.Controller.Info())
	case r.URL.Path == "/api/renew" && r.Method == "POST":
		if err := s.Controller.Renew(r.Context()); err != nil {
			uiError(w, 409, "RENEW_FAILED", err.Error())
			return
		}
		uiJSON(w, 200, s.Controller.Info())
	case r.URL.Path == "/api/pair-text" && r.Method == "GET":
		text, err := s.Controller.PairText()
		if err != nil {
			uiError(w, 409, "PAIR_UNAVAILABLE", err.Error())
			return
		}
		uiJSON(w, 200, map[string]string{"pairingText": text})
	case r.URL.Path == "/api/pair" && r.Method == "GET":
		b, err := s.Controller.Pair()
		if err != nil {
			uiError(w, 409, "PAIR_UNAVAILABLE", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="pair.json"`)
		_, _ = w.Write(b)
	case r.URL.Path == "/api/serial-ports" && r.Method == "GET":
		ports, err := serialport.List()
		if err != nil {
			uiError(w, 500, err.Code, err.Message)
			return
		}
		uiJSON(w, 200, map[string]any{"ports": ports})
	case r.URL.Path == "/api/rpc" && r.Method == "POST":
		var req protocol.Request
		if !decodeUI(w, r, &req) {
			return
		}
		if req.ID == "" {
			req.ID = identity.Random(12)
		}
		req.Version = 1
		result, rpcErr := s.Controller.Call(r.Context(), member, req)
		uiJSON(w, 200, protocol.Response{Version: 1, ID: req.ID, Result: result, Error: rpcErr})
	case r.URL.Path == "/api/shutdown" && r.Method == "POST":
		uiJSON(w, 200, map[string]bool{"stopping": true})
		s.cancel()
	default:
		uiError(w, 404, "NOT_FOUND", "接口不存在或方法不支持")
	}
}
func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		uiError(w, 405, "METHOD_NOT_ALLOWED", "不支持的方法")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	contentType := ""
	switch name {
	case "index.html":
		contentType = "text/html; charset=utf-8"
	case "app.js", "debug.js", "vendor/xterm.js", "vendor/addon-fit.js":
		contentType = "text/javascript; charset=utf-8"
	case "icon.svg":
		contentType = "image/svg+xml"
	case "style.css", "vendor/xterm.css":
		contentType = "text/css; charset=utf-8"
	default:
		http.NotFound(w, r)
		return
	}
	b, err := files.ReadFile("web/" + name)
	if err != nil {
		http.Error(w, "UI asset unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", contentType)
	if r.Method != "HEAD" {
		_, _ = w.Write(b)
	}
}
func (s *Server) newSession() (string, browserSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) >= 32 {
		return "", browserSession{}, false
	}
	id := identity.Random(32)
	session := browserSession{CSRF: identity.Random(32), Seen: time.Now()}
	s.sessions[id] = session
	return id, session, true
}
func (s *Server) open(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		uiError(w, 400, "INVALID_OPEN", "启动请求无效")
		return
	}
	token := r.PostForm.Get("token")
	s.mu.Lock()
	item, valid := s.tokens[token]
	if valid {
		delete(s.tokens, token)
	}
	s.mu.Unlock()
	if valid {
		_ = os.Remove(item.File)
	}
	if !valid || time.Now().After(item.Expires) {
		uiError(w, 401, "OPEN_EXPIRED", "界面启动请求已过期，请重新打开程序")
		return
	}
	id, session, ok := s.newSession()
	if !ok {
		uiError(w, 429, "SESSION_LIMIT", "界面会话数量已达上限")
		return
	}
	index, err := files.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "UI unavailable", 500)
		return
	}
	nonce := identity.Random(16)
	bootstrap, _ := json.Marshal(map[string]string{"token": id, "csrfToken": session.CSRF})
	script := `<script nonce="` + nonce + `">window.__AGENT_SESSION__=` + string(bootstrap) + `;history.replaceState(null,'','/');</script>`
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'nonce-"+nonce+"'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(strings.Replace(string(index), "<!--AGENT_SESSION-->", script, 1)))
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if time.Now().After(s.loginReset) {
		s.loginFailures = 0
		s.loginReset = time.Now().Add(time.Minute)
	}
	if s.loginFailures >= 10 {
		s.mu.Unlock()
		uiError(w, 429, "RATE_LIMITED", "尝试过多，请一分钟后重试")
		return
	}
	s.loginFailures++
	secret := s.descriptor.Secret
	s.mu.Unlock()
	var body struct {
		Token string `json:"token"`
	}
	if !decodeUI(w, r, &body) {
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(body.Token)), []byte(secret)) != 1 {
		uiError(w, 401, "AUTH_FAILED", "界面访问文件不正确")
		return
	}
	id, session, ok := s.newSession()
	if !ok {
		uiError(w, 429, "SESSION_LIMIT", "界面会话数量已达上限")
		return
	}
	uiJSON(w, 200, map[string]any{"token": id, "csrfToken": session.CSRF})
}
func (s *Server) reap() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			var expired []string
			for id, session := range s.sessions {
				if time.Since(session.Seen) > 5*time.Minute {
					delete(s.sessions, id)
					expired = append(expired, id)
				}
			}
			s.mu.Unlock()
			for _, id := range expired {
				s.Controller.Detach("local-ui/" + id)
				s.network.closeOwner("local-ui/" + id)
			}
		}
	}
}
func uiSameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Scheme == "http" && u.Host == r.Host && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && u.User == nil
}
func decodeUI(w http.ResponseWriter, r *http.Request, out any) bool {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		uiError(w, 415, "INVALID_CONTENT_TYPE", "请使用 JSON 请求")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, protocol.MaxFrame))
	dec.DisallowUnknownFields()
	if dec.Decode(out) != nil {
		uiError(w, 400, "INVALID_INPUT", "请求内容无效")
		return false
	}
	if dec.Decode(new(any)) != io.EOF {
		uiError(w, 400, "INVALID_INPUT", "请求格式无效")
		return false
	}
	return true
}
func uiJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func uiError(w http.ResponseWriter, status int, code, message string) {
	uiJSON(w, status, map[string]any{"error": protocol.Fail(code, message)})
}

func (s *Server) serveNetwork(w http.ResponseWriter, r *http.Request, owner string) {
	if r.URL.Path == "/api/network/open" && r.Method == "POST" {
		var p networkOpen
		if !decodeUI(w, r, &p) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		session, err := s.network.open(ctx, owner, p)
		if err != nil {
			uiError(w, 400, "NETWORK_OPEN_FAILED", err.Error())
			return
		}
		uiJSON(w, 200, session.snapshot(0))
		return
	}
	if r.URL.Path == "/api/network/read" && r.Method == "GET" {
		cursor, err := strconv.ParseUint(r.URL.Query().Get("cursor"), 10, 64)
		if err != nil {
			uiError(w, 400, "INVALID_CURSOR", "网络记录游标无效")
			return
		}
		session, err := s.network.get(owner, r.URL.Query().Get("id"))
		if err != nil {
			uiError(w, 404, "NETWORK_NOT_FOUND", err.Error())
			return
		}
		uiJSON(w, 200, session.snapshot(cursor))
		return
	}
	if r.Method != "POST" || (r.URL.Path != "/api/network/send" && r.URL.Path != "/api/network/close" && r.URL.Path != "/api/network/disconnect") {
		uiError(w, 404, "NOT_FOUND", "接口不存在或方法不支持")
		return
	}
	var p struct {
		ID     string `json:"id"`
		Peer   string `json:"peer"`
		Remote string `json:"remote"`
		Data   []byte `json:"data"`
	}
	if !decodeUI(w, r, &p) {
		return
	}
	session, err := s.network.get(owner, p.ID)
	if err != nil {
		uiError(w, 404, "NETWORK_NOT_FOUND", err.Error())
		return
	}
	switch r.URL.Path {
	case "/api/network/send":
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		err = session.send(ctx, p.Peer, p.Remote, p.Data)
	case "/api/network/close":
		err = s.network.remove(owner, p.ID)
	case "/api/network/disconnect":
		err = session.disconnect(p.Peer)
	}
	if err != nil {
		uiError(w, 400, "NETWORK_ERROR", err.Error())
		return
	}
	uiJSON(w, 200, map[string]bool{"ok": true})
}
