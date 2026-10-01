package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// Emitter stamps events with a sequence number and timestamp and writes them
// as JSONL to every sink. It is safe for use by concurrent workers.
type Emitter struct {
	mu    sync.Mutex
	runID string
	seq   int64
	sinks []io.Writer
	// listeners are called in order, one event at a time.
	listeners []func(Event)
	now       func() time.Time
}

func NewEmitter(runID string, sinks ...io.Writer) *Emitter {
	return &Emitter{runID: runID, sinks: sinks, now: time.Now}
}

// Listen registers fn to receive every event after it is written. Register
// listeners before emitting; fn must not call back into the emitter.
func (e *Emitter) Listen(fn func(Event)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.listeners = append(e.listeners, fn)
}

// Emit writes a typed payload. A nil emitter discards it.
func (e *Emitter) Emit(stage Stage, p Payload) error {
	if e == nil {
		return nil
	}
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", p.Kind(), err)
	}
	return e.write(stage, p.Kind(), data)
}

// EmitRaw relays an event produced by the agent subprocess. The payload is
// validated against the contract before it reaches the stream.
func (e *Emitter) EmitRaw(stage Stage, kind Kind, data json.RawMessage) error {
	if e == nil {
		return nil
	}
	if _, err := Decode(kind, data); err != nil {
		return err
	}
	return e.write(stage, kind, data)
}

func (e *Emitter) write(stage Stage, kind Kind, data json.RawMessage) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	ev := Event{
		V:     Version,
		Seq:   e.seq,
		TS:    e.now().UTC(),
		RunID: e.runID,
		Stage: stage,
		Kind:  kind,
		Data:  data,
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	var errs []error
	for _, w := range e.sinks {
		if _, err := w.Write(line); err != nil {
			errs = append(errs, err)
		}
	}
	for _, fn := range e.listeners {
		fn(ev)
	}
	return errors.Join(errs...)
}
