package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/pion/webrtc/v4"
	"sitedeck/internal/filelock"
	"sitedeck/internal/identity"
	"sitedeck/internal/transport"
)

// RuntimeOptions is shared by the CLI and desktop controller.
type RuntimeOptions struct {
	StateFile        string
	RegistrationFile string
	Registration     string
	Name             string
	Hub              string
	Allow            string
	ICEFile          string
	LocalOnly        bool
	Observer         func(Event)
	OperationsReady  func(*Operations)
}
type Event struct {
	Time      time.Time `json:"time"`
	Type      string    `json:"type"`
	State     string    `json:"state,omitempty"`
	Message   string    `json:"message,omitempty"`
	Channel   string    `json:"channelId,omitempty"`
	Transport string    `json:"transport,omitempty"`
}

func (c Config) notify(event Event) {
	if c.Observer != nil {
		event.Time = time.Now().UTC()
		c.Observer(event)
	}
}

func RunStored(ctx context.Context, options RuntimeOptions) error {
	lease, err := filelock.Acquire(options.StateFile + ".run.lock")
	if err != nil {
		return err
	}
	defer lease.Close()
	registration := ""
	var ice []webrtc.ICEServer
	if !options.LocalOnly {
		if err = transport.ValidateEndpoint(options.Hub); err != nil {
			return err
		}
		registration = options.Registration
		if registration == "" {
			registration, err = identity.ReadSecret(options.RegistrationFile)
			if err != nil {
				return err
			}
		}
	}
	if options.ICEFile != "" {
		b, err := os.ReadFile(options.ICEFile)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(b, &ice); err != nil {
			return err
		}
	}
	for ctx.Err() == nil {
		state, err := identity.Load(options.StateFile)
		if err != nil {
			return err
		}
		if !options.LocalOnly && !state.GuestExpiresAt.IsZero() && !time.Now().Before(state.GuestExpiresAt) {
			return fmt.Errorf("现场授权已到期，请先续期或轮换访问秘钥")
		}
		if options.Name != "" {
			state.Name = options.Name
		}
		runCtx, cancel := context.WithCancel(ctx)
		changed := make(chan struct{})
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-runCtx.Done():
					return
				case <-ticker.C:
					next, err := identity.Load(options.StateFile)
					if err != nil || next.GuestToken != state.GuestToken || next.Certificate != state.Certificate || !next.GuestExpiresAt.Equal(state.GuestExpiresAt) || (!options.LocalOnly && !state.GuestExpiresAt.IsZero() && !time.Now().Before(state.GuestExpiresAt)) {
						close(changed)
						cancel()
						return
					}
				}
			}
		}()
		err = Run(runCtx, Config{Hub: options.Hub, Registration: registration, State: state, Allow: options.Allow, ICE: ice, LocalOnly: options.LocalOnly, Observer: options.Observer, OperationsReady: options.OperationsReady})
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-changed:
			if options.Observer != nil {
				options.Observer(Event{Time: time.Now().UTC(), Type: "credentials.changed", Message: "访问凭据已更新，原连接已撤销"})
			}
		default:
			return err
		}
	}
	return nil
}
