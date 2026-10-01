// Package server lets the dashboard watch runs. Every `heall run` writes its
// event stream to <runs dir>/<run id>/events.jsonl as it goes; the server
// lists those runs and streams a run's file to the browser, following it
// while the run is still in progress. It never talks to the run itself, so a
// run needs no flag to be watchable and a finished run replays the same way
// as a live one.
package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"heall/internal/events"
)

const eventsFile = "events.jsonl"

type Server struct {
	// RunsDir holds one directory per run.
	RunsDir string
	// WebDir is the built dashboard to serve at /; empty serves the API only.
	WebDir string
	// Poll is how often a run in progress is checked for new events.
	Poll time.Duration
	// Idle is how long a run may go without a new event before the stream
	// gives up on it: a run that crashed never writes run_done.
	Idle time.Duration
}

// RunInfo is one row of the run list.
type RunInfo struct {
	ID      string    `json:"id"`
	Started time.Time `json:"started"`
	Branch  string    `json:"branch"`
	Bad     string    `json:"bad"`
	Test    string    `json:"test"`
	// Outcome is "fixed", "escalated" or "error"; empty while running.
	Outcome string `json:"outcome"`
	Events  int    `json:"events"`
}

var runID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/runs", s.listRuns)
	mux.HandleFunc("GET /api/runs/{id}/events", s.streamRun)
	if s.WebDir != "" {
		mux.Handle("/", http.FileServer(http.Dir(s.WebDir)))
	}
	return allowLocalOrigins(mux)
}

// allowLocalOrigins lets a dashboard served from another local port, such
// as the dev server, read the API. Pages on other sites get no access.
func allowLocalOrigins(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			if u, err := url.Parse(origin); err == nil && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1") {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.RunsDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	runs := []RunInfo{}
	for _, e := range entries {
		if !e.IsDir() || !runID.MatchString(e.Name()) {
			continue
		}
		if info, ok := summarize(filepath.Join(s.RunsDir, e.Name(), eventsFile), e.Name()); ok {
			runs = append(runs, info)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Started.After(runs[j].Started) })
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(runs)
}

// summarize reads a run's events for the fields the run list shows.
func summarize(path, id string) (RunInfo, bool) {
	f, err := os.Open(path)
	if err != nil {
		return RunInfo{}, false
	}
	defer f.Close()
	info := RunInfo{ID: id}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var ev events.Event
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if info.Events == 0 {
			info.Started = ev.TS
		}
		info.Events++
		switch ev.Kind {
		case events.KindRunStarted:
			var d events.RunStarted
			if json.Unmarshal(ev.Data, &d) == nil {
				info.Branch, info.Bad = d.Branch, d.Bad
			}
		case events.KindTriageDone:
			var d events.TriageDone
			if json.Unmarshal(ev.Data, &d) == nil {
				info.Test = d.TestName
			}
		case events.KindRunDone:
			var d events.RunDone
			if json.Unmarshal(ev.Data, &d) == nil {
				info.Outcome = d.Outcome
			}
		}
	}
	return info, info.Events > 0
}

// streamRun sends a run's events as server-sent events: everything written
// so far at once, then each new event as the run writes it. It ends with an
// "end" event once run_done has been sent.
func (s *Server) streamRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !runID.MatchString(id) {
		http.Error(w, "invalid run id", http.StatusBadRequest)
		return
	}
	f, err := os.Open(filepath.Join(s.RunsDir, id, eventsFile))
	if err != nil {
		http.Error(w, "no such run", http.StatusNotFound)
		return
	}
	defer f.Close()
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}
	// A browser that reconnects says which event it saw last.
	after, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	poll, idle := s.Poll, s.Idle
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	if idle <= 0 {
		idle = 20 * time.Minute
	}
	var pending []byte
	buf := make([]byte, 64*1024)
	lastData := time.Now()
	for {
		n, err := f.Read(buf)
		if n > 0 {
			lastData = time.Now()
			pending = append(pending, buf[:n]...)
			// Only whole lines are events; the run may be mid-write.
			for {
				i := bytes.IndexByte(pending, '\n')
				if i < 0 {
					break
				}
				line := bytes.TrimSpace(pending[:i])
				pending = pending[i+1:]
				if len(line) == 0 {
					continue
				}
				var ev events.Event
				if json.Unmarshal(line, &ev) != nil {
					continue
				}
				if ev.Seq > after {
					fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.Seq, line)
				}
				if ev.Kind == events.KindRunDone {
					fmt.Fprint(w, "event: end\ndata: {}\n\n")
					flusher.Flush()
					return
				}
			}
			flusher.Flush()
			continue
		}
		if err != nil && err != io.EOF {
			return
		}
		if time.Since(lastData) > idle {
			fmt.Fprint(w, "event: end\ndata: {\"reason\":\"the run stopped writing events\"}\n\n")
			flusher.Flush()
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(poll):
		}
	}
}

// FindWebDir looks for the built dashboard: $HEALL_WEB_DIR, then web/out
// beside the program's directory, then web/out in the working directory.
func FindWebDir() string {
	var candidates []string
	if dir := os.Getenv("HEALL_WEB_DIR"); dir != "" {
		candidates = append(candidates, dir)
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "..", "web", "out"))
	}
	candidates = append(candidates, filepath.Join("web", "out"))
	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
			if abs, err := filepath.Abs(dir); err == nil {
				return abs
			}
		}
	}
	return ""
}

// Addr formats a listen address that is reachable from this machine only.
func Addr(port int) string {
	return "127.0.0.1:" + strings.TrimSpace(strconv.Itoa(port))
}
