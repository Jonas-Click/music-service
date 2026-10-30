package main

import (
	"bytes"
	"testing"
)

type rawCommandSink struct {
	id    uint32
	data  []byte
	sends int
}

func (s *rawCommandSink) send(id uint32, data []byte) error {
	s.id, s.data = id, append([]byte(nil), data...)
	s.sends++
	return nil
}
func (*rawCommandSink) recv() (uint32, []byte, error) { return 0, nil, errNoRx }
func (*rawCommandSink) describe() string              { return "raw-command-test" }
func (*rawCommandSink) close()                        {}

func TestRawCommandPreservesPayload(t *testing.T) {
	for _, test := range []struct {
		payload string
		id      uint32
		want    []byte
	}{
		{"raw:4EA:023200", 0x4EA, []byte{2, 50, 0}},
		{"raw:4EA:003200", 0x4EA, []byte{0, 50, 0}},
		{"raw:4E8:00", 0x4E8, []byte{0}},
		{"raw:4E8", 0x4E8, []byte{1}},
	} {
		t.Run(test.payload, func(t *testing.T) {
			sink := &rawCommandSink{}
			handleCommand(sink, nil, test.payload)
			if sink.sends != 1 || !bytes.Equal(sink.data, test.want) {
				t.Fatalf("got %d sends, % X; want % X", sink.sends, sink.data, test.want)
			}
			if sink.id != test.id {
				t.Fatalf("wrong CAN ID: %X", sink.id)
			}
		})
	}
}

func TestRawCommandRejectsMalformedPayload(t *testing.T) {
	for _, payload := range []string{"raw", "raw:not-hex:00", "raw:4EA:zz", "raw:4EA:0"} {
		sink := &rawCommandSink{}
		handleCommand(sink, nil, payload)
		if sink.sends != 0 {
			t.Fatalf("malformed command sent a frame: %q", payload)
		}
	}
}
