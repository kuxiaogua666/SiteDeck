package session

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sitedeck/internal/protocol"
	"testing"
	"time"
)

func TestSerialEventsKeepBinaryAndOriginalTimestamp(t *testing.T) {
	var out bytes.Buffer
	s := New("serial", "test", &out, io.NopCloser(bytes.NewReader(nil)), nil)
	defer s.Close()
	before := time.Now().UTC()
	s.Attach("test", false)
	rx := []byte{0, 0xff, 0x1b, 13}
	tx := []byte{0x55, 0xaa, 0}
	s.Write(rx)
	s.Write(rx)
	if err := s.Input("test", tx); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC()
	r, err := s.Read(context.Background(), "", 1024, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Events) != 3 {
		t.Fatal("distinct receive times were merged")
	}
	for _, e := range r.Events {
		if e.Time.Before(before) || e.Time.After(after) {
			t.Fatal("timestamp was not captured on I/O")
		}
	}
	if !bytes.Equal(r.Events[0].Data, rx) || !bytes.Equal(r.Events[2].Data, tx) || !bytes.Equal(out.Bytes(), tx) {
		t.Fatal("binary RX/TX changed")
	}
	repeated, err := s.Read(context.Background(), "", 1024, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !repeated.Events[0].Time.Equal(r.Events[0].Time) {
		t.Fatal("timestamp changed when history was reread")
	}
}

func TestSerialTimestampPagesFitRPCFrame(t *testing.T) {
	s := New("serial", "test", io.Discard, io.NopCloser(bytes.NewReader(nil)), nil)
	defer s.Close()
	for i := 0; i < 4096; i++ {
		s.Write([]byte{byte(i)})
	}
	cursor := ""
	total := 0
	for total < 4096 {
		result, err := s.Read(context.Background(), cursor, 48<<10, 0)
		if err != nil {
			t.Fatal(err)
		}
		encoded, errJSON := json.Marshal(protocol.Response{Version: 1, ID: "test", Result: result})
		if errJSON != nil || len(encoded) > protocol.MaxFrame {
			t.Fatal("serial response exceeds frame", len(encoded), errJSON)
		}
		if len(result.Data) == 0 {
			t.Fatal("cursor failed to progress")
		}
		total += len(result.Data)
		cursor = result.Next
	}
}
