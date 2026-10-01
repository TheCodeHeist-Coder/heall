package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func event(seq int, kind, data string) string {
	return fmt.Sprintf(`{"v":1,"seq":%d,"ts":"2026-10-01T10:00:%02d.000Z","run_id":"r","stage":"run","kind":"%s","data":%s}`+"\n",
		seq, seq, kind, data)
}

func setup(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	web := filepath.Join(t.TempDir(), "web")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<title>heall</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{RunsDir: dir, Web: os.DirFS(web), Poll: 10 * time.Millisecond, Idle: 400 * time.Millisecond}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, dir
}

func writeRun(t *testing.T, dir, id, content string) string {
	t.Helper()
	path := filepath.Join(dir, id, eventsFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func appendTo(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

// sse reads server-sent events as "name:payload" strings ("message" for
// plain data events).
func sse(t *testing.T, body io.Reader, out chan<- string) {
	sc := bufio.NewScanner(body)
	sc.Buffer(nil, 1<<20)
	name := "message"
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			out <- name + ":" + strings.TrimPrefix(line, "data: ")
			name = "message"
		}
	}
	close(out)
}

func next(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case v, ok := <-ch:
		if !ok {
			return "<closed>"
		}
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for an event")
		return ""
	}
}

func TestListRuns(t *testing.T) {
	ts, dir := setup(t)
	writeRun(t, dir, "r-old", event(1, "run_started", `{"repo":"/r","branch":"main","good":"g","bad":"b1","dry_run":true}`)+
		event(2, "triage_done", `{"test_name":"adds","test_file":"t.js","suspect_files":[],"excerpt":""}`)+
		event(3, "run_done", `{"outcome":"fixed","duration_ms":5}`))
	writeRun(t, dir, "r-new", strings.Replace(event(1, "run_started", `{"repo":"/r","branch":"bug/x","good":"g","bad":"b2","dry_run":false}`), "10:00:01", "11:00:00", 1))
	writeRun(t, dir, "r-empty", "")
	if err := os.MkdirAll(filepath.Join(dir, "not-a-run"), 0o755); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(ts.URL + "/api/runs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var runs []RunInfo
	if err := json.NewDecoder(resp.Body).Decode(&runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ID != "r-new" || runs[1].ID != "r-old" {
		t.Fatalf("runs, newest first: %+v", runs)
	}
	if r := runs[1]; r.Outcome != "fixed" || r.Test != "adds" || r.Branch != "main" || r.Bad != "b1" || r.Events != 3 {
		t.Errorf("finished run: %+v", r)
	}
	if r := runs[0]; r.Outcome != "" || r.Branch != "bug/x" {
		t.Errorf("run in progress: %+v", r)
	}

	// An empty runs directory is an empty list, not an error.
	empty := httptest.NewServer((&Server{RunsDir: filepath.Join(dir, "missing")}).Handler())
	defer empty.Close()
	resp2, err := http.Get(empty.URL + "/api/runs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	if strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("got %q, want []", body)
	}
}

func TestStreamFollowsARunInProgress(t *testing.T) {
	ts, dir := setup(t)
	path := writeRun(t, dir, "r1", event(1, "stage_started", `{}`)+event(2, "log", `{"level":"info","message":"one"}`))

	resp, err := http.Get(ts.URL + "/api/runs/r1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	ch := make(chan string, 16)
	go sse(t, resp.Body, ch)

	// What is already written arrives straight away.
	for _, want := range []string{`"seq":1`, `"seq":2`} {
		if got := next(t, ch); !strings.Contains(got, want) {
			t.Fatalf("got %s, want an event with %s", got, want)
		}
	}
	// A half-written line is held back until the run finishes writing it.
	whole := event(3, "log", `{"level":"info","message":"two"}`)
	appendTo(t, path, whole[:40])
	select {
	case got := <-ch:
		t.Fatalf("a partial line was sent: %s", got)
	case <-time.After(80 * time.Millisecond):
	}
	appendTo(t, path, whole[40:])
	if got := next(t, ch); !strings.Contains(got, `"message":"two"`) {
		t.Fatalf("got %s", got)
	}
	// run_done ends the stream.
	appendTo(t, path, event(4, "run_done", `{"outcome":"fixed","duration_ms":1}`))
	if got := next(t, ch); !strings.Contains(got, `"kind":"run_done"`) {
		t.Fatalf("got %s", got)
	}
	if got := next(t, ch); got != "end:{}" {
		t.Fatalf("got %s, want the end event", got)
	}
	if got := next(t, ch); got != "<closed>" {
		t.Fatalf("the stream stayed open after run_done: %s", got)
	}
}

func TestStreamResumesAfterLastEventID(t *testing.T) {
	ts, dir := setup(t)
	writeRun(t, dir, "r1", event(1, "stage_started", `{}`)+event(2, "stage_started", `{}`)+event(3, "run_done", `{"outcome":"error","duration_ms":1}`))
	req, _ := http.NewRequest("GET", ts.URL+"/api/runs/r1/events", nil)
	req.Header.Set("Last-Event-ID", "2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), `"seq":1`) || strings.Contains(string(body), `"seq":2`) || !strings.Contains(string(body), "id: 3\n") {
		t.Errorf("a reconnect should get only what it missed:\n%s", body)
	}
}

func TestStreamGivesUpOnARunThatWentSilent(t *testing.T) {
	ts, dir := setup(t)
	writeRun(t, dir, "r1", event(1, "stage_started", `{}`))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/runs/r1/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("the stream never ended: %v", err)
	}
	if !strings.Contains(string(body), "event: end") || !strings.Contains(string(body), "stopped writing") {
		t.Errorf("body:\n%s", body)
	}
}

func TestRunIDsCannotLeaveTheRunsDirectory(t *testing.T) {
	ts, dir := setup(t)
	secret := filepath.Join(filepath.Dir(dir), "outside")
	writeRun(t, filepath.Dir(dir), "outside", event(1, "log", `{"level":"info","message":"secret"}`))
	defer os.RemoveAll(secret)
	for _, id := range []string{"..%2Foutside", "%2e%2e%2foutside", "..", ".", "a%2F..%2F..%2Foutside"} {
		resp, err := http.Get(ts.URL + "/api/runs/" + id + "/events")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK || strings.Contains(string(body), "secret") {
			t.Errorf("%s: status %d, body %q", id, resp.StatusCode, body)
		}
	}
	resp, _ := http.Get(ts.URL + "/api/runs/nope/events")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown run: status %d", resp.StatusCode)
	}
}

func TestServesTheDashboardAndOnlyLocalOrigins(t *testing.T) {
	ts, _ := setup(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "<title>heall</title>") {
		t.Errorf("index: %q", body)
	}

	for origin, allowed := range map[string]bool{
		"http://localhost:3000":     true,
		"http://127.0.0.1:3000":     true,
		"https://evil.example":      false,
		"http://localhost.evil.com": false,
	} {
		req, _ := http.NewRequest("GET", ts.URL+"/api/runs", nil)
		req.Header.Set("Origin", origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Access-Control-Allow-Origin"); (got == origin) != allowed {
			t.Errorf("origin %s: Access-Control-Allow-Origin = %q, allowed should be %v", origin, got, allowed)
		}
	}
}
