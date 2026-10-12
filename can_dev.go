package main

// Development sinks: no CAN hardware required. Selected with --device log:
// or --device null:, for exercising the command and Redis logic without a
// scooter attached.

import "log"

type logSink struct{}

func newLogSink() *logSink { return &logSink{} }

func (s *logSink) send(id uint32, data []byte) error {
	log.Printf("can(log): id=0x%X data=% X", id, data)
	return nil
}
func (s *logSink) recv() (uint32, []byte, error) { return 0, nil, errNoRx }
func (s *logSink) describe() string              { return "log:" }
func (s *logSink) close()                        {}

type nullSink struct{}

func newNullSink() *nullSink                          { return &nullSink{} }
func (s *nullSink) send(id uint32, data []byte) error { return nil }
func (s *nullSink) recv() (uint32, []byte, error)     { return 0, nil, errNoRx }
func (s *nullSink) describe() string                  { return "null:" }
func (s *nullSink) close()                            {}
