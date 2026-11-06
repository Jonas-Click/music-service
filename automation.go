package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

type AutomationConfig struct {
	Inputs []AutomationInput `json:"inputs"`
	Rules  []AutomationRule  `json:"rules"`
}

func decodeAutomationJSON(data []byte, target any) error {
	if len(data) > 65536 {
		return fmt.Errorf("automation configuration exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON")
	}
	return nil
}

func (c *AutomationConfig) UnmarshalJSON(data []byte) error {
	type plain AutomationConfig
	return decodeAutomationJSON(data, (*plain)(c))
}

type AutomationInput struct {
	Hash   string   `json:"hash"`
	Fields []string `json:"fields"`
}

type AutomationRule struct {
	Name    string             `json:"name"`
	On      string             `json:"on"`
	When    string             `json:"when,omitempty"`
	Actions []AutomationAction `json:"actions"`
}

type AutomationAction struct {
	Do       string `json:"do"`
	When     string `json:"when,omitempty"`
	Song     *int   `json:"song,omitempty"`
	Volume   *int   `json:"volume,omitempty"`
	Style    string `json:"style,omitempty"`
	Duration string `json:"duration,omitempty"`
	Title    string `json:"title,omitempty"`
	Body     string `json:"body,omitempty"`
}

type automationEnv struct {
	Topic string                      `expr:"topic"`
	Src   string                      `expr:"src"`
	From  string                      `expr:"from"`
	To    string                      `expr:"to"`
	Data  map[string]any              `expr:"data"`
	State func(string, string) string `expr:"state"`
}

type compiledAction struct {
	action AutomationAction
	when   *vm.Program
}
type compiledRule struct {
	name, on string
	when     *vm.Program
	actions  []compiledAction
}
type automationEngine struct {
	fields          map[string][]string
	rules           []compiledRule
	audioBusy       atomic.Bool
	audioGeneration atomic.Uint64
}
type automationSnapshot map[string]map[string]string
type automationSession struct {
	engine *automationEngine
	last   automationSnapshot
}
type automationEvent struct{ topic, from, to, hash, field string }

var automation *automationEngine

func compileAutomationWhen(source string) (*vm.Program, error) {
	if source == "" {
		return nil, nil
	}
	if len(source) > 4096 {
		return nil, fmt.Errorf("condition exceeds 4096 bytes")
	}
	return expr.Compile(source, expr.Env(automationEnv{}), expr.AsBool(), expr.MaxNodes(256))
}

func automationName(s string) bool {
	return s != "" && len(s) <= 128 && !strings.ContainsAny(s, " \t\n\r\x00*?[]")
}

func validateAutomationInputs(inputs []AutomationInput) (map[string][]string, error) {
	fields := map[string][]string{vehicleHash: {engineStateField}}
	count := 1
	for _, input := range inputs {
		if !automationName(input.Hash) || input.Hash == cmdChannel {
			return nil, fmt.Errorf("invalid input hash %q", input.Hash)
		}
		if len(input.Fields) == 0 {
			return nil, fmt.Errorf("input %q needs fields", input.Hash)
		}
		for _, field := range input.Fields {
			if !automationName(field) {
				return nil, fmt.Errorf("invalid input field %q", field)
			}
			found := false
			for _, old := range fields[input.Hash] {
				if old == field {
					found = true
					break
				}
			}
			if !found {
				fields[input.Hash] = append(fields[input.Hash], field)
				count++
			}
		}
	}
	if len(fields) > 16 || count > 64 || len(inputs) > 16 {
		return nil, fmt.Errorf("automation inputs exceed 16 hashes or 64 fields")
	}
	return fields, nil
}

func validateAutomationAction(a AutomationAction) error {
	if a.Volume != nil {
		if *a.Volume < 0 || *a.Volume > 100 {
			return fmt.Errorf("volume must be 0-100")
		}
		switch a.Do {
		case "volume", "engine.volume", "engine.start", "engine.on", "engine.off", "engine.ride", "engine.for":
		default:
			return fmt.Errorf("volume is not valid for %s", a.Do)
		}
	}
	if a.Song != nil && (a.Do != "play" || *a.Song < 1 || *a.Song > 255) {
		return fmt.Errorf("song is only valid for play and must be 1-255")
	}
	if a.Style != "" {
		if a.Do != "engine.style" {
			return fmt.Errorf("style is only valid for engine.style")
		}
		if _, ok := engineStyleID(a.Style); !ok {
			return fmt.Errorf("unknown engine style %q", a.Style)
		}
	}
	if a.Duration != "" && a.Do != "engine.for" {
		return fmt.Errorf("duration is only valid for engine.for")
	}
	if (a.Title != "" || a.Body != "") && a.Do != "notify" {
		return fmt.Errorf("title and body are only valid for notify")
	}
	if len(a.Title) > 120 || len(a.Body) > 512 {
		return fmt.Errorf("notification text is too long")
	}
	switch a.Do {
	case "play":
		if a.Song == nil {
			return fmt.Errorf("play needs song")
		}
	case "volume", "engine.volume":
		if a.Volume == nil {
			return fmt.Errorf("%s needs volume", a.Do)
		}
	case "engine.style":
		if a.Style == "" {
			return fmt.Errorf("engine.style needs style")
		}
	case "engine.for":
		d, err := time.ParseDuration(a.Duration)
		if err != nil || d < engineMinDuration || d > engineMaxDuration {
			return fmt.Errorf("engine.for needs duration between 10s and 24h")
		}
	case "engine.start", "engine.on", "engine.off", "engine.ride", "engine.demo", "stop", "engine.cycle", "notify":
	default:
		return fmt.Errorf("unknown automatic action %q", a.Do)
	}
	return nil
}

func compileAutomationRules(rules []AutomationRule) ([]compiledRule, error) {
	if len(rules) > 32 {
		return nil, fmt.Errorf("automation exceeds 32 rules")
	}
	names := map[string]bool{}
	var compiled []compiledRule
	for _, rule := range rules {
		if !automationName(rule.Name) || names[rule.Name] {
			return nil, fmt.Errorf("invalid or duplicate rule name %q", rule.Name)
		}
		names[rule.Name] = true
		if rule.On == "" || len(rule.On) > 256 || strings.ContainsAny(strings.TrimSuffix(rule.On, "*"), "*?[] \t\n\r\x00") {
			return nil, fmt.Errorf("rule %s: invalid topic", rule.Name)
		}
		if len(rule.Actions) == 0 || len(rule.Actions) > 8 {
			return nil, fmt.Errorf("rule %s needs 1-8 actions", rule.Name)
		}
		p, err := compileAutomationWhen(rule.When)
		if err != nil {
			return nil, fmt.Errorf("rule %s: %w", rule.Name, err)
		}
		c := compiledRule{name: rule.Name, on: rule.On, when: p}
		for i, action := range rule.Actions {
			if err := validateAutomationAction(action); err != nil {
				return nil, fmt.Errorf("rule %s action %d: %w", rule.Name, i, err)
			}
			p, err := compileAutomationWhen(action.When)
			if err != nil {
				return nil, fmt.Errorf("rule %s action %d: %w", rule.Name, i, err)
			}
			c.actions = append(c.actions, compiledAction{action: action, when: p})
		}
		compiled = append(compiled, c)
	}
	return compiled, nil
}

func compileAutomation(c AutomationConfig) (*automationEngine, error) {
	fields, err := validateAutomationInputs(c.Inputs)
	if err != nil {
		return nil, err
	}
	rules, err := compileAutomationRules(c.Rules)
	if err != nil {
		return nil, err
	}
	for _, rule := range rules {
		observed := false
		for hash, names := range fields {
			for _, name := range names {
				observed = observed || automationMatches(rule.on, hash+"."+name+".changed")
			}
		}
		if !observed {
			return nil, fmt.Errorf("rule %s: topic %q has no declared input", rule.name, rule.on)
		}
	}
	return &automationEngine{fields: fields, rules: rules}, nil
}

func (a *automationEngine) channels() []string {
	var channels []string
	if a != nil && len(a.rules) > 0 {
		for hash := range a.fields {
			if hash != vehicleChan {
				channels = append(channels, hash)
			}
		}
	}
	sort.Strings(channels)
	return channels
}

func (a *automationEngine) snapshot(conn *redisConn) (automationSnapshot, error) {
	snapshot := automationSnapshot{}
	for hash, fields := range a.fields {
		values, err := conn.hmget(hash, fields...)
		if err != nil {
			return nil, err
		}
		if len(values) != len(fields) {
			return nil, fmt.Errorf("incomplete snapshot of %s", hash)
		}
		snapshot[hash] = map[string]string{}
		for i, field := range fields {
			if len(values[i]) > 4096 {
				return nil, fmt.Errorf("input %s.%s exceeds 4096 bytes", hash, field)
			}
			snapshot[hash][field] = values[i]
		}
	}
	return snapshot, nil
}

func (a *automationEngine) session(conn *redisConn) (*automationSession, error) {
	if a == nil || len(a.rules) == 0 {
		return nil, nil
	}
	snapshot, err := a.snapshot(conn)
	if err != nil {
		return nil, err
	}
	return &automationSession{engine: a, last: snapshot}, nil
}

func (s *automationSession) observe(conn *redisConn, hash, field string) (automationSnapshot, []automationEvent, error) {
	snapshot, err := s.engine.snapshot(conn)
	if err != nil {
		return nil, nil, err
	}
	var events []automationEvent
	for _, watched := range s.engine.fields[hash] {
		if field != watched && field != "cleared" && field != "replaced" {
			continue
		}
		old, value := s.last[hash][watched], snapshot[hash][watched]
		if old != value {
			events = append(events, automationEvent{topic: hash + "." + watched + ".changed", from: old, to: value, hash: hash, field: watched})
		}
		// Only the notified field advances its event baseline; companion reads
		// must not consume an edge before that field's notification arrives.
		s.last[hash][watched] = value
	}
	return snapshot, events, nil
}

func automationMatches(pattern, topic string) bool {
	return pattern == topic || strings.HasSuffix(pattern, "*") && strings.HasPrefix(topic, strings.TrimSuffix(pattern, "*"))
}

func automationEval(p *vm.Program, event automationEvent, snapshot automationSnapshot) (bool, error) {
	if p == nil {
		return true, nil
	}
	env := automationEnv{Topic: event.topic, Src: "adapter", From: event.from, To: event.to, Data: map[string]any{"hash": event.hash, "field": event.field}, State: func(hash, field string) string { return snapshot[hash][field] }}
	evaluator := vm.VM{MemoryBudget: 4096}
	value, err := evaluator.Run(p, env)
	if err != nil {
		return false, err
	}
	result, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("condition did not return bool")
	}
	return result, nil
}

func (s *automationSession) execute(sink canSink, conn *redisConn, events []automationEvent, snapshot automationSnapshot) {
	for _, event := range events {
		for _, rule := range s.engine.rules {
			if !automationMatches(rule.on, event.topic) {
				continue
			}
			match, err := automationEval(rule.when, event, snapshot)
			if err != nil {
				log.Printf("automation %s: condition: %v", rule.name, err)
				continue
			}
			if !match {
				continue
			}
			for _, step := range rule.actions {
				match, err := automationEval(step.when, event, snapshot)
				if err != nil {
					log.Printf("automation %s: action condition: %v", rule.name, err)
					break
				}
				if !match {
					continue
				}
				if err := s.engine.runAction(sink, conn, step.action); err != nil {
					log.Printf("automation %s: %v", rule.name, err)
					break
				}
				log.Printf("automation %s: %s", rule.name, step.action.Do)
			}
		}
	}
}

type automaticSink struct {
	canSink
	current func() bool
}

func (s automaticSink) send(id uint32, data []byte) error {
	if !s.current() {
		return context.Canceled
	}
	return s.canSink.send(id, data)
}

func (a *automationEngine) backgroundAudio(sink canSink, action func(canSink)) error {
	if !a.audioBusy.CompareAndSwap(false, true) {
		return fmt.Errorf("automatic audio action already running")
	}
	generation := a.audioGeneration.Add(1)
	guarded := automaticSink{canSink: sink, current: func() bool { return a.audioGeneration.Load() == generation }}
	go func() { defer a.audioBusy.Store(false); action(guarded) }()
	return nil
}

func cancelAutomaticAudio() {
	if automation != nil {
		automation.audioGeneration.Add(1)
	}
}

func (runner *automationEngine) runAction(sink canSink, conn *redisConn, a AutomationAction) error {
	volume := -1
	if a.Volume != nil {
		volume = *a.Volume
	}
	switch a.Do {
	case "engine.start":
		_, state := engineSnapshot()
		if state != vehicleStateReady {
			return fmt.Errorf("engine.start requires ready-to-drive")
		}
		engineApply(sink, engineInterval, 0, volume)
	case "engine.cycle":
		s, _ := engineSnapshot()
		engineSetStyle(sink, (s.style+1)%len(engineStyles))
	case "engine.style":
		id, _ := engineStyleID(a.Style)
		engineSetStyle(sink, id)
	case "engine.volume":
		s, _ := engineSnapshot()
		engineSet(sink, s.mode, time.Until(s.until), volume)
	case "engine.on":
		engineSet(sink, engineOn, 0, volume)
	case "engine.off":
		engineSet(sink, engineOff, 0, volume)
	case "engine.ride":
		engineSet(sink, engineRide, 0, volume)
	case "engine.for":
		d, _ := time.ParseDuration(a.Duration)
		engineSet(sink, engineTimed, d, volume)
	case "engine.demo":
		return runner.backgroundAudio(sink, func(guarded canSink) { engineDemo(guarded) })
	case "play":
		if !validSong(*a.Song) {
			return fmt.Errorf("unknown song %d", *a.Song)
		}
		song, _ := songByNumber(*a.Song)
		return runner.backgroundAudio(sink, func(guarded canSink) {
			if err := uploadSong(guarded, song, uploadMaxAttempts); err != nil {
				log.Printf("automation play: %v", err)
				return
			}
			if err := sendPlay(guarded, *a.Song); err != nil {
				log.Printf("automation play: %v", err)
				return
			}
			confirmPlaying(guarded, *a.Song)
		})
	case "stop":
		runner.audioGeneration.Add(1)
		handleCommand(sink, conn, "stop")
	case "volume":
		setMusicVolume(sink, volume)
	case "notify":
		title := a.Title
		if title == "" {
			s, state := engineSnapshot()
			title = "Engine sound off"
			if s.sounding(time.Now(), state) {
				title = "Engine sound: " + []string{"V8", "Moped", "Sport", "V-Twin", "Electric"}[s.style]
			}
		}
		payload, err := json.Marshal(map[string]any{"source": "music-service", "id": "automation", "title": title, "body": a.Body, "severity": "info", "ttl_ms": 8000})
		if err != nil {
			return err
		}
		// Publication is immediate; it reports the requested action, not an ECU echo.
		_, err = conn.publish("scootui:notification", string(payload))
		return err
	}
	return nil
}
