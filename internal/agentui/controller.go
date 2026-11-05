package agentui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"sitedeck/internal/agent"
	"sitedeck/internal/agentconfig"
	"sitedeck/internal/identity"
	"sitedeck/internal/pairing"
	"sitedeck/internal/protocol"
)

type execution struct {
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	ready     chan struct{}
	readyOnce sync.Once
}
type Controller struct {
	Directory  string
	configPath string
	opMu       sync.Mutex
	mu         sync.Mutex
	settings   Settings
	active     *execution
	ops        *agent.Operations
	status     string
	lastError  string
	generation int
	channels   map[string]agent.Event
	events     []agent.Event
}

func NewController(directory string) (*Controller, error) {
	return NewControllerWithConfig(directory, "")
}
func NewControllerWithConfig(directory, configPath string) (*Controller, error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	if configPath == "" {
		configPath, err = agentconfig.Discover(directory)
		if err != nil {
			return nil, err
		}
	}
	if configPath == "" {
		configPath = filepath.Join(directory, "agent-ui.json")
	}
	configPath, err = filepath.Abs(configPath)
	if err != nil {
		return nil, err
	}
	settings, err := LoadSettings(configPath)
	if os.IsNotExist(err) {
		settings = DefaultSettings()
	} else if err != nil {
		return nil, err
	}
	if _, err = identity.Init(filepath.Join(directory, "agent.json"), time.Duration(settings.GuestTTLHours)*time.Hour); err != nil {
		return nil, err
	}
	return &Controller{Directory: directory, configPath: configPath, settings: settings, status: "stopped", channels: map[string]agent.Event{}, events: []agent.Event{}}, nil
}
func (c *Controller) ConfigPath() string { return c.configPath }
func (c *Controller) StatePath() string  { return filepath.Join(c.Directory, "agent.json") }
func (c *Controller) Settings() SettingsView {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.settings.View()
}
func (c *Controller) ShouldAutoStart() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.settings.AutoStart
}
func (c *Controller) appendEventLocked(event agent.Event) {
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	if len(event.Message) > 2048 {
		event.Message = strings.ToValidUTF8(event.Message[:2048], "�")
	}
	c.events = append(c.events, event)
	if len(c.events) > 256 {
		c.events = append([]agent.Event(nil), c.events[len(c.events)-256:]...)
	}
}
func (c *Controller) Info() map[string]any {
	c.mu.Lock()
	settings := c.settings.View()
	status, lastError, generation := c.status, c.lastError, c.generation
	active := c.active != nil
	ops := c.ops
	channels := make([]agent.Event, 0, len(c.channels))
	for _, event := range c.channels {
		channels = append(channels, event)
	}
	events := append([]agent.Event{}, c.events...)
	c.mu.Unlock()
	sort.Slice(channels, func(i, j int) bool { return channels[i].Time.Before(channels[j].Time) })
	value := map[string]any{"settings": settings, "running": active, "ready": ops != nil, "state": status, "lastError": lastError, "generation": generation, "channels": channels, "events": events, "version": protocol.Version, "os": runtime.GOOS, "arch": runtime.GOARCH, "dataDirectory": c.Directory, "configPath": c.ConfigPath()}
	if state, err := identity.Load(c.StatePath()); err == nil {
		value["deviceId"] = state.Device
		value["fingerprint"] = state.Fingerprint()
		value["guestExpiresAt"] = state.GuestExpiresAt
		value["guestExpired"] = !time.Now().Before(state.GuestExpiresAt)
	}
	if ops != nil {
		value["sessions"] = ops.Sessions.List()
	} else {
		value["sessions"] = []any{}
	}
	return value
}
func (c *Controller) Save(ctx context.Context, settings Settings, restart bool) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	settings.Version = 1
	settings.Name = strings.TrimSpace(settings.Name)
	settings.Hub = strings.TrimSpace(settings.Hub)
	settings.RegistrationToken = strings.TrimSpace(settings.RegistrationToken)
	c.mu.Lock()
	wasRunning := c.active != nil
	if settings.RegistrationToken == "" {
		settings.RegistrationToken = c.settings.RegistrationToken
	}
	c.mu.Unlock()
	if err := ValidateSettings(settings); err != nil {
		return err
	}
	if wasRunning && !restart {
		return fmt.Errorf("请先停止现场服务，或选择保存并重新连接")
	}
	if wasRunning {
		if err := c.stopLocked(ctx); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err = writePrivate(c.ConfigPath(), append(b, '\n')); err != nil {
		return err
	}
	c.mu.Lock()
	c.settings = settings
	c.lastError = ""
	c.appendEventLocked(agent.Event{Type: "settings.saved", Message: "连接配置和现场权限已保存"})
	c.mu.Unlock()
	if wasRunning {
		return c.startLocked(ctx)
	}
	return nil
}
func (c *Controller) Start(ctx context.Context) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	return c.startLocked(ctx)
}
func (c *Controller) startLocked(ctx context.Context) error {
	c.mu.Lock()
	if c.active != nil {
		c.mu.Unlock()
		return nil
	}
	settings := c.settings
	c.mu.Unlock()
	if settings.RemoteEnabled {
		if settings.Hub == "" {
			return fmt.Errorf("请先填写 Hub 地址，或关闭远程接入以使用本机模式")
		}
		if settings.RegistrationToken == "" {
			return fmt.Errorf("请先导入 Hub 注册凭据 hub.key")
		}
	}
	if err := ValidateSettings(settings); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	run := &execution{ctx: runCtx, cancel: cancel, done: make(chan struct{}), ready: make(chan struct{})}
	c.mu.Lock()
	c.active = run
	c.status = "starting"
	c.lastError = ""
	c.channels = map[string]agent.Event{}
	c.mu.Unlock()
	go func() {
		err := agent.RunStored(runCtx, agent.RuntimeOptions{StateFile: c.StatePath(), Hub: settings.Hub, Name: settings.Name, Registration: settings.RegistrationToken, Allow: strings.Join(settings.Permissions, ","), ICEFile: settings.ICEFile, LocalOnly: !settings.RemoteEnabled, Observer: func(event agent.Event) {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.active != run {
				return
			}
			if event.Type == "state" {
				c.status = event.State
				if event.State == "reconnecting" {
					c.lastError = event.Message
				} else {
					c.lastError = ""
				}
			}
			if event.Type == "channel.ready" {
				c.channels[event.Channel] = event
			}
			if event.Type == "channel.closed" {
				delete(c.channels, event.Channel)
			}
			c.appendEventLocked(event)
		}, OperationsReady: func(ops *agent.Operations) {
			c.mu.Lock()
			if c.active == run {
				c.ops = ops
				c.generation++
				c.channels = map[string]agent.Event{}
			}
			c.mu.Unlock()
			run.readyOnce.Do(func() { close(run.ready) })
		}})
		run.cancel()
		c.mu.Lock()
		if c.active == run {
			c.active = nil
			c.ops = nil
			c.channels = map[string]agent.Event{}
			c.status = "stopped"
			if err != nil {
				c.status = "error"
				c.lastError = err.Error()
				c.appendEventLocked(agent.Event{Type: "error", Message: err.Error()})
			} else {
				c.appendEventLocked(agent.Event{Type: "state", State: "stopped", Message: "现场服务已停止，活动资源已关闭"})
			}
		}
		c.mu.Unlock()
		close(run.done)
	}()
	select {
	case <-run.ready:
		return nil
	case <-run.done:
		c.mu.Lock()
		message := c.lastError
		c.mu.Unlock()
		if message == "" {
			message = "现场服务未能启动"
		}
		return fmt.Errorf("%s", message)
	case <-ctx.Done():
		run.cancel()
		return ctx.Err()
	case <-time.After(3 * time.Second):
		return nil
	}
}
func (c *Controller) Stop(ctx context.Context) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	return c.stopLocked(ctx)
}
func (c *Controller) stopLocked(ctx context.Context) error {
	c.mu.Lock()
	run := c.active
	if run != nil {
		c.status = "stopping"
		run.cancel()
	}
	c.mu.Unlock()
	if run == nil {
		return nil
	}
	select {
	case <-run.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(12 * time.Second):
		return fmt.Errorf("现场资源仍在关闭，请等待停止完成")
	}
}
func (c *Controller) Renew(ctx context.Context) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	wasRunning := c.active != nil
	ttl := c.settings.GuestTTLHours
	c.mu.Unlock()
	if err := c.stopLocked(ctx); err != nil {
		return err
	}
	s, err := identity.Load(c.StatePath())
	if err != nil {
		return err
	}
	s.GuestToken = identity.Random(32)
	s.GuestExpiresAt = time.Now().Add(time.Duration(ttl) * time.Hour)
	if err = s.Save(c.StatePath()); err != nil {
		return err
	}
	c.mu.Lock()
	c.appendEventLocked(agent.Event{Type: "credentials.changed", Message: "访客秘钥已轮换并续期，旧配对文件已失效"})
	c.mu.Unlock()
	if wasRunning {
		return c.startLocked(ctx)
	}
	return nil
}
func (c *Controller) Pair() ([]byte, error) {
	s, err := identity.Load(c.StatePath())
	if err != nil {
		return nil, err
	}
	if !time.Now().Before(s.GuestExpiresAt) {
		return nil, fmt.Errorf("现场授权已到期，请先轮换并续期")
	}
	c.mu.Lock()
	hub := c.settings.Hub
	c.mu.Unlock()
	if hub == "" {
		return nil, fmt.Errorf("请先保存 Hub 地址后再导出配对文件")
	}
	return json.MarshalIndent(map[string]any{"hub": hub, "deviceId": s.Device, "fingerprint": s.Fingerprint(), "token": s.GuestToken, "transport": "auto", "guestExpiresAt": s.GuestExpiresAt}, "", "  ")
}
func (c *Controller) Call(ctx context.Context, member string, req protocol.Request) (any, *protocol.Error) {
	c.mu.Lock()
	run, ops := c.active, c.ops
	c.mu.Unlock()
	if run == nil || ops == nil || run.ctx.Err() != nil {
		return nil, protocol.Fail("SERVICE_STOPPED", "请先启动现场服务")
	}
	callCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(run.ctx, cancel)
	defer cancel()
	defer stop()
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if timeout > 5*time.Minute {
		timeout = 5 * time.Minute
	}
	callCtx, deadline := context.WithTimeout(callCtx, timeout)
	defer deadline()
	result, rpcErr := ops.Handle(callCtx, member, map[string]any{"transport": "local-ui"}, req, ops.LocalPermissions())
	if !strings.HasSuffix(req.Method, ".read") && req.Method != "session.list" && req.Method != "permissions.get" {
		code := "OK"
		if rpcErr != nil {
			code = rpcErr.Code
		}
		c.mu.Lock()
		c.appendEventLocked(agent.Event{Type: "operation", Message: req.Method + " · " + code})
		c.mu.Unlock()
	}
	return result, rpcErr
}
func (c *Controller) Detach(member string) {
	c.mu.Lock()
	ops := c.ops
	c.mu.Unlock()
	if ops != nil {
		ops.Sessions.Detach(member)
	}
}
func (c *Controller) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return c.Stop(ctx)
}

func (c *Controller) PairText() (string, error) {
	data, err := c.Pair()
	if err != nil {
		return "", err
	}
	var p pairing.Profile
	if err = json.Unmarshal(data, &p); err != nil {
		return "", err
	}
	return pairing.Encode(p)
}
