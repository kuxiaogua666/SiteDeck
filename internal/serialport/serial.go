package serialport

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"

	"go.bug.st/serial"
	"sitedeck/internal/protocol"
	"sitedeck/internal/session"
)

type Params struct {
	Port     string `json:"port"`
	Baud     int    `json:"baudRate"`
	DataBits int    `json:"dataBits"`
	StopBits int    `json:"stopBits"`
	Parity   string `json:"parity"`
}

func List() ([]string, *protocol.Error) {
	ports, err := serial.GetPortsList()
	if err != nil {
		return nil, protocol.Fail("SERIAL_ENUM_FAILED", err.Error())
	}
	if ports == nil {
		ports = []string{}
	}
	return ports, nil
}
func Canonical(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("serial port is required")
	}
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name), nil
	}
	return filepath.EvalSymlinks(name)
}
func Open(p Params) (*session.Session, *protocol.Error) {
	if p.Baud == 0 {
		p.Baud = 115200
	}
	if p.DataBits == 0 {
		p.DataBits = 8
	}
	if p.StopBits == 0 {
		p.StopBits = 1
	}
	if p.Baud < 1 || p.Baud > 4000000 || p.DataBits < 5 || p.DataBits > 8 || p.StopBits < 1 || p.StopBits > 2 {
		return nil, protocol.Fail("INVALID_PARAMS", "invalid serial parameters")
	}
	parity := serial.NoParity
	switch strings.ToLower(p.Parity) {
	case "", "none":
	case "odd":
		parity = serial.OddParity
	case "even":
		parity = serial.EvenParity
	case "mark":
		parity = serial.MarkParity
	case "space":
		parity = serial.SpaceParity
	default:
		return nil, protocol.Fail("INVALID_PARAMS", "invalid parity")
	}
	stop := serial.OneStopBit
	if p.StopBits == 2 {
		stop = serial.TwoStopBits
	}
	port, err := serial.Open(p.Port, &serial.Mode{BaudRate: p.Baud, DataBits: p.DataBits, StopBits: stop, Parity: parity})
	if err != nil {
		code := "SERIAL_OPEN_FAILED"
		var portError *serial.PortError
		if errors.As(err, &portError) {
			switch portError.Code() {
			case serial.PortBusy:
				code = "RESOURCE_BUSY"
			case serial.PortNotFound:
				code = "DEVICE_NOT_FOUND"
			case serial.PermissionDenied:
				code = "PERMISSION_DENIED"
			case serial.InvalidSerialPort, serial.InvalidSpeed, serial.InvalidDataBits, serial.InvalidParity, serial.InvalidStopBits:
				code = "INVALID_PARAMS"
			}
		}
		if errors.Is(err, fs.ErrPermission) {
			code = "PERMISSION_DENIED"
		}
		if errors.Is(err, fs.ErrNotExist) {
			code = "DEVICE_NOT_FOUND"
		}
		return nil, protocol.Fail(code, err.Error())
	}
	s := session.New("serial", p.Port, port, port, nil)
	go func() {
		b := make([]byte, 8192)
		for {
			n, err := port.Read(b)
			if n > 0 {
				_, _ = s.Write(b[:n])
			}
			if err != nil {
				s.End(-1)
				return
			}
		}
	}()
	return s, nil
}
