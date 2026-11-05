package protocol

import (
	"encoding/json"
	"fmt"
)

const Version = "0.5.1"
const DefaultHubAddress = "127.0.0.1:48763"
const DefaultHubURL = "ws://127.0.0.1:48763/ws"
const MaxFrame = 128 << 10
const MaxOutput = 1 << 20

type Signal struct {
	AccessProtocol int      `json:"accessProtocol,omitempty"`
	Name           string   `json:"name,omitempty"`
	OS             string   `json:"os,omitempty"`
	Arch           string   `json:"arch,omitempty"`
	Version        string   `json:"version,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
	SigningKey     string   `json:"signingKey,omitempty"`
	AccessToken    string   `json:"accessToken,omitempty"`
	AccessTicket   string   `json:"accessTicket,omitempty"`
	AccessRequired bool     `json:"accessRequired,omitempty"`
	Transport      string   `json:"transport,omitempty"`
	Attempt        string   `json:"attempt,omitempty"`
	Policy         string   `json:"policy,omitempty"`
	Type           string   `json:"type"`
	Device         string   `json:"deviceId,omitempty"`
	Channel        string   `json:"channelId,omitempty"`
	SDP            string   `json:"sdp,omitempty"`
	Data           []byte   `json:"data,omitempty"`
	Reason         string   `json:"reason,omitempty"`
}

type Request struct {
	ClientID  string          `json:"clientId,omitempty"`
	Version   int             `json:"version"`
	ID        string          `json:"id"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params"`
	TimeoutMS int             `json:"timeoutMs"`
}

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *Error) Error() string         { return e.Code + ": " + e.Message }
func Fail(code, message string) *Error { return &Error{Code: code, Message: message} }

type Response struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

func Decode(raw json.RawMessage, out any) *Error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return Fail("INVALID_PARAMS", fmt.Sprintf("invalid parameters: %v", err))
	}
	return nil
}
