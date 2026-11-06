package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAutomationExample(t *testing.T) {
	data, err := os.ReadFile("examples/left-brake-engine.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := loadConfigFrom(t, string(data))
	if err != nil {
		t.Fatal(err)
	}
	if *c.EngineSound.Enabled || len(c.compiledAutomation.rules) != 3 {
		t.Fatal("example must use per-interval admission")
	}
}

func TestAutomationExpressionsUseSnapshot(t *testing.T) {
	p, err := compileAutomationWhen("from == 'parked' and to == 'ready-to-drive' and state('vehicle', 'brake:left') == 'on' and src == 'adapter'")
	if err != nil {
		t.Fatal(err)
	}
	event := automationEvent{topic: "vehicle.state.changed", from: "parked", to: "ready-to-drive", hash: "vehicle", field: "state"}
	for _, brake := range []string{"on", "off", ""} {
		snapshot := automationSnapshot{"vehicle": {"state": "ready-to-drive", "brake:left": brake}}
		ok, err := automationEval(p, event, snapshot)
		if err != nil || ok != (brake == "on") {
			t.Fatal(brake, ok, err)
		}
	}
	p, err = compileAutomationWhen("float(to) > 50 and float(from) <= 50")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := automationEval(p, automationEvent{from: "50", to: "51"}, nil)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err = automationEval(p, automationEvent{from: "bad", to: "51"}, nil); err == nil {
		t.Fatal("invalid conversion succeeded")
	}
}

func TestAutomationRejectsInvalidConfiguration(t *testing.T) {
	cases := []string{
		`{"automation":{"rules":[{"name":"r","on":"vehicle.state.changed","when":"brak == 'on'","actions":[{"do":"engine.start"}]}]}}`,
		`{"automation":{"rules":[{"name":"r","on":"vehicle.state.changed","when":"42","actions":[{"do":"engine.start"}]}]}}`,
		`{"automation":{"rules":[{"name":"r","on":"vehicle.state.changed","whne":"false","actions":[{"do":"engine.start"}]}]}}`,
		`{"automation":{"rules":[{"name":"r","on":"vehicle.state.changed","actions":[{"do":"raw"}]}]}}`,
		`{"automation":{"rules":[{"name":"r","on":"vehicle.state.changed","actions":[{"do":"exec"}]}]}}`,
		`{"automation":{"rules":[{"name":"r","on":"vehicle.state.changed","actions":[{"do":"engine.style","style":"unknown"}]}]}}`,
		`{"automation":{"rules":[{"name":"r","on":"vehicle.state.changed","actions":[{"do":"play","song":0}]}]}}`,
		`{"automation":{"rules":[{"name":"r","on":"missing.state.changed","actions":[{"do":"notify"}]}]}}`,
		`{"automation":{"inputs":[{"hash":"music","fields":["state"]}]}}`,
		`{"automation":{"inputs":[{"hash":"vehicle","fields":["*"]}]}}`,
		`{"automation":{"rules":[{"name":"r","on":"vehicle.state.changed","actions":[{"do":"notify","volume":50}]}]}}`,
	}
	for _, text := range cases {
		if _, err := loadConfigFrom(t, text); err == nil {
			t.Fatal("accepted", text)
		}
	}
	c, err := loadConfigFrom(t, `{}`)
	if err != nil || len(c.compiledAutomation.rules) != 0 {
		t.Fatal(c, err)
	}
}

func TestAutomationInputAndRuleLimits(t *testing.T) {
	fields := make([]string, 65)
	for i := range fields {
		fields[i] = strings.Repeat("x", i+1)
	}
	if _, err := validateAutomationInputs([]AutomationInput{{Hash: "h", Fields: fields}}); err == nil {
		t.Fatal("unbounded fields")
	}
	_, err := compileAutomationRules([]AutomationRule{{Name: "r", On: "vehicle.*", Actions: []AutomationAction{{Do: "stop"}}}, {Name: "r", On: "vehicle.*", Actions: []AutomationAction{{Do: "stop"}}}})
	if err == nil {
		t.Fatal("duplicate name")
	}
	if _, err := compileAutomationWhen(strings.Repeat("x", 4097)); err == nil {
		t.Fatal("unbounded expression")
	}
	if !automationMatches("vehicle.*", "vehicle.state.changed") || automationMatches("battery.*", "vehicle.state.changed") {
		t.Fatal("topic match")
	}
}

func TestAutomationActionsValidateParameters(t *testing.T) {
	pct := 40
	song := 240
	for _, action := range []AutomationAction{
		{Do: "play", Song: &song}, {Do: "stop"}, {Do: "volume", Volume: &pct},
		{Do: "engine.start"}, {Do: "engine.on"}, {Do: "engine.off"}, {Do: "engine.ride"},
		{Do: "engine.for", Duration: "30s"}, {Do: "engine.volume", Volume: &pct},
		{Do: "engine.style", Style: "electric"}, {Do: "engine.cycle"}, {Do: "engine.demo"},
		{Do: "notify", Title: "Sound enabled"},
	} {
		if err := validateAutomationAction(action); err != nil {
			t.Fatal(action, err)
		}
	}
	for _, action := range []AutomationAction{{Do: "volume"}, {Do: "engine.for", Duration: "1s"}, {Do: "stop", Style: "v8"}, {Do: "engine.style", Style: "v8", Volume: &pct}} {
		if err := validateAutomationAction(action); err == nil {
			t.Fatal("accepted", action)
		}
	}
}

func TestAutomaticAudioIsSingleFlightAndCancelable(t *testing.T) {
	runner := &automationEngine{}
	old := automation
	automation = runner
	defer func() { automation = old }()
	release := make(chan struct{})
	done := make(chan error, 1)
	sink := &rawCommandSink{}
	if err := runner.backgroundAudio(sink, func(guarded canSink) { <-release; done <- guarded.send(0x4E8, []byte{240}) }); err != nil {
		t.Fatal(err)
	}
	if err := runner.backgroundAudio(sink, func(canSink) {}); err == nil {
		t.Fatal("queued overlapping audio")
	}
	cancelAutomaticAudio()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for runner.audioBusy.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if runner.audioBusy.Load() || sink.sends != 0 {
		t.Fatal("canceled audio sent a frame or stayed busy")
	}
}

func TestAutomaticIntervalIsArmedOnlyWhileSelected(t *testing.T) {
	s := engineState{mode: engineInterval}
	if !s.armed(time.Now()) || !s.sounding(time.Now(), vehicleStateReady) || s.sounding(time.Now(), vehicleStateParked) {
		t.Fatal("interval state")
	}
}
