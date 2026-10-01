package events

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"sync"
	"testing"
)

const examples = "../../../contracts/examples/events.jsonl"

// The example stream is shared with the Python and TypeScript tests, so a
// field renamed in one language fails here.
func TestExampleStreamMatchesContract(t *testing.T) {
	f, err := os.Open(examples)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	seen := map[Kind]bool{}
	var lastSeq int64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var ev Event
		dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&ev); err != nil {
			t.Fatalf("envelope: %v\n%s", err, sc.Text())
		}
		if ev.V != Version {
			t.Errorf("seq %d: version %d, want %d", ev.Seq, ev.V, Version)
		}
		if ev.Seq != lastSeq+1 {
			t.Errorf("seq %d follows %d", ev.Seq, lastSeq)
		}
		lastSeq = ev.Seq
		p, err := Decode(ev.Kind, ev.Data)
		if err != nil {
			t.Fatalf("seq %d: %v", ev.Seq, err)
		}
		if p.Kind() != ev.Kind {
			t.Errorf("seq %d: payload kind %s, envelope kind %s", ev.Seq, p.Kind(), ev.Kind)
		}
		seen[ev.Kind] = true
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	for _, k := range Kinds() {
		if !seen[k] {
			t.Errorf("no example for kind %q", k)
		}
	}
}

func TestDecodeRejectsDrift(t *testing.T) {
	if _, err := Decode("nope", json.RawMessage(`{}`)); err == nil {
		t.Error("unknown kind accepted")
	}
	if _, err := Decode(KindLog, json.RawMessage(`{"level":"info","message":"x","extra":1}`)); err == nil {
		t.Error("unknown field accepted")
	}
}

func TestEmitterSequencesConcurrentWriters(t *testing.T) {
	var buf bytes.Buffer
	em := NewEmitter("r1", &buf)

	const n = 200
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := em.Emit(StageLocate, CommitTesting{SHA: "abc", Round: 1, Worker: i}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	sc := bufio.NewScanner(&buf)
	var want int64
	for sc.Scan() {
		want++
		var ev Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("line %d is not one JSON object: %v", want, err)
		}
		if ev.Seq != want || ev.RunID != "r1" || ev.Kind != KindCommitTesting {
			t.Fatalf("line %d: got seq=%d run=%s kind=%s", want, ev.Seq, ev.RunID, ev.Kind)
		}
	}
	if want != n {
		t.Fatalf("got %d lines, want %d", want, n)
	}
}

func TestEmitRawValidates(t *testing.T) {
	var buf bytes.Buffer
	em := NewEmitter("r1", &buf)
	if err := em.EmitRaw(StageHeal, KindAgentThought, json.RawMessage(`{"text":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	if err := em.EmitRaw(StageHeal, KindAgentThought, json.RawMessage(`{"txt":"hi"}`)); err == nil {
		t.Error("malformed agent payload was relayed")
	}
	if got := bytes.Count(buf.Bytes(), []byte("\n")); got != 1 {
		t.Errorf("wrote %d lines, want 1", got)
	}
}
