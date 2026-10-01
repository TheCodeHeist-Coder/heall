"use client";

// Where the dashboard gets a run from, and how it plays one back.

import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";

import { parseEvent, type HeallEvent } from "./events";
import { fold, parseStream, replayDelay } from "./run-state";

export type RunInfo = {
  id: string;
  started: string;
  branch: string;
  bad: string;
  test: string;
  outcome: string;
  events: number;
};

export type Source =
  | { kind: "server"; id: string }
  | { kind: "sample"; name: string }
  | { kind: "file"; name: string; events: HeallEvent[] };

export const SAMPLES = [
  { name: "off-by-one", label: "Fixed: off-by-one bug" },
  { name: "outdated-test", label: "Escalated: outdated test" },
  { name: "flaky", label: "Escalated: flaky test" },
];

// `heall serve` hands out the dashboard and the API from one address. The
// dev server runs on its own port, so there the API is on heall's default.
export function apiBase(): string {
  if (process.env.NEXT_PUBLIC_HEALL_API) return process.env.NEXT_PUBLIC_HEALL_API;
  if (typeof window !== "undefined" && window.location.port === "3000") {
    return "http://localhost:7777";
  }
  return "";
}

// useQuery reads the page's query string, so a link can open a given run:
// ?run=<id>, ?sample=<name>, and ?theme=dark or light.
export function useQuery(): URLSearchParams {
  const search = useSyncExternalStore(
    () => () => {},
    () => window.location.search,
    () => "",
  );
  return useMemo(() => new URLSearchParams(search), [search]);
}

// useRuns polls the list of runs recorded on this machine. `online` is false
// when there is no heall server, as when the dashboard is opened as plain
// files; the recorded samples still work then.
export function useRuns(): { runs: RunInfo[]; online: boolean | null } {
  const [runs, setRuns] = useState<RunInfo[]>([]);
  const [online, setOnline] = useState<boolean | null>(null);
  useEffect(() => {
    let stopped = false;
    const load = async () => {
      try {
        const res = await fetch(`${apiBase()}/api/runs`, { cache: "no-store" });
        if (!res.ok) throw new Error(String(res.status));
        const list = (await res.json()) as RunInfo[];
        if (stopped) return;
        setRuns(list);
        setOnline(true);
      } catch {
        if (!stopped) setOnline(false);
      }
    };
    load();
    const timer = setInterval(load, 2000);
    return () => {
      stopped = true;
      clearInterval(timer);
    };
  }, []);
  return { runs, online };
}

type Loaded = { key: string; events: HeallEvent[]; live: boolean; error: string };

export function sourceKey(source: Source | null): string {
  if (!source) return "";
  return source.kind === "server" ? `server:${source.id}` : `${source.kind}:${source.name}`;
}

// useEvents loads the events of a source. A run on the server is streamed,
// so one that is still in progress keeps growing; `live` says so.
export function useEvents(source: Source | null): Loaded {
  const key = sourceKey(source);
  const [loaded, setLoaded] = useState<Loaded>({ key: "", events: [], live: false, error: "" });

  useEffect(() => {
    // A file's events are already in hand; see the return below.
    if (!source || source.kind === "file") return;
    // What is held belongs to this source, or is left over from another.
    const current = (prev: Loaded): Loaded => (prev.key === key ? prev : { key, events: [], live: true, error: "" });
    if (source.kind === "sample") {
      let stopped = false;
      fetch(`samples/${source.name}.jsonl`)
        .then((res) => (res.ok ? res.text() : Promise.reject(new Error(String(res.status)))))
        .then((text) => {
          if (!stopped) setLoaded({ key, events: parseStream(text, parseEvent), live: false, error: "" });
        })
        .catch(() => {
          if (!stopped) setLoaded({ key, events: [], live: false, error: "The sample could not be loaded." });
        });
      return () => {
        stopped = true;
      };
    }

    const stream = new EventSource(`${apiBase()}/api/runs/${encodeURIComponent(source.id)}/events`);
    let lastSeq = 0;
    stream.onmessage = (msg) => {
      let ev: HeallEvent;
      try {
        ev = parseEvent(msg.data);
      } catch {
        return;
      }
      // A reconnect may resend; the sequence number says what is new.
      if (ev.seq <= lastSeq) return;
      lastSeq = ev.seq;
      setLoaded((prev) => ({ ...current(prev), events: [...current(prev).events, ev] }));
    };
    stream.addEventListener("end", () => {
      stream.close();
      setLoaded((prev) => ({ ...current(prev), live: false }));
    });
    stream.onerror = () => {
      // EventSource retries by itself while the run is in progress. A closed
      // stream means the server is gone.
      if (stream.readyState === EventSource.CLOSED) {
        setLoaded((prev) => ({ ...current(prev), live: false, error: "Lost the connection to heall serve." }));
      }
    };
    return () => stream.close();
    // The key stands for the source: a file's events never change under it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  if (source?.kind === "file") return { key, events: source.events, live: false, error: "" };
  return loaded.key === key ? loaded : { key, events: [], live: source?.kind === "server", error: "" };
}

export type Playback = {
  // How many events are on screen.
  shown: number;
  replaying: boolean;
  paused: boolean;
  speed: number;
  replay: () => void;
  togglePause: () => void;
  seek: (n: number) => void;
  setSpeed: (speed: number) => void;
};

// usePlayback decides how much of a run is on screen. Normally that is all
// of it, so a live run is followed as it happens. A replay starts again from
// nothing and lets the events arrive with their recorded pacing.
//
// startAt opens the run paused at that many events, for links that point at
// a moment in a run (?at=42).
export function usePlayback(events: HeallEvent[], key: string, startAt: number | null = null): Playback {
  const [position, setPosition] = useState<{ key: string; at: number | null } | null>(null);
  const [pausedByUser, setPaused] = useState<boolean | null>(null);
  const [speed, setSpeed] = useState(1);
  // Until the reader touches the controls, the link decides.
  const untouched = position === null;
  const at = untouched ? startAt : position.key === key ? position.at : null;
  const paused = pausedByUser ?? (untouched && startAt !== null);
  const replaying = at !== null;

  useEffect(() => {
    if (at === null || paused) return;
    // Showing the last event ends the replay; the run is then simply shown.
    const timer = setTimeout(
      () =>
        setPosition((p) => {
          const now = p === null ? startAt : p.key === key ? p.at : null;
          if (now === null) return p;
          return now + 1 >= events.length ? { key, at: null } : { key, at: now + 1 };
        }),
      replayDelay(events, at, speed),
    );
    return () => clearTimeout(timer);
  }, [at, paused, speed, events, key, startAt]);

  const replay = useCallback(() => {
    if (events.length === 0) return;
    setPaused(false);
    setPosition({ key, at: 0 });
  }, [key, events.length]);
  const togglePause = useCallback(() => setPaused(!paused), [paused]);
  const seek = useCallback(
    (n: number) => {
      setPaused(true);
      setPosition({ key, at: Math.max(0, Math.min(n, events.length)) });
    },
    [key, events.length],
  );

  return {
    shown: at === null ? events.length : Math.min(at, events.length),
    replaying,
    paused: replaying && paused,
    speed,
    replay,
    togglePause,
    seek,
    setSpeed,
  };
}

export function useRunState(events: HeallEvent[], shown: number) {
  return useMemo(() => fold(events, shown), [events, shown]);
}

// useStickToBottom keeps a scrolling list at its end while new items arrive,
// unless the reader has scrolled up to look at something.
export function useStickToBottom<T extends HTMLElement>(count: number) {
  const ref = useRef<T>(null);
  const stuck = useRef(true);
  useEffect(() => {
    const el = ref.current;
    if (el && stuck.current) el.scrollTop = el.scrollHeight;
  }, [count]);
  const onScroll = useCallback(() => {
    const el = ref.current;
    if (el) stuck.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  }, []);
  return { ref, onScroll };
}
