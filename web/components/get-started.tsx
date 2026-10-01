"use client";

import { useEffect, useRef, useState } from "react";

const STEPS: { title: string; text: React.ReactNode; command: string }[] = [
  {
    title: "Install heall",
    text: "One program, for Linux and macOS. Your machine also needs git, Docker and Python 3.10 or newer.",
    command: "curl -fsSL https://heall.rexial.in/install | sh",
  },
  {
    title: "Add your Groq key",
    text: (
      <>
        heall uses a Groq model to propose fixes. Get a free key at{" "}
        <a href="https://console.groq.com" target="_blank" rel="noreferrer" className="text-accent underline-offset-2 hover:underline">
          console.groq.com
        </a>{" "}
        and put it in place of <span className="font-mono">YOUR_KEY</span>.
      </>
    ),
    command: 'mkdir -p ~/.config/heall && echo "GROQ_API_KEY=YOUR_KEY" >> ~/.config/heall/env',
  },
  {
    title: "Connect your repository",
    text: "Run this inside your project. It writes a short .heall.yaml and checks that your machine is ready.",
    command: "heall init",
  },
  {
    title: "Open the dashboard",
    text: (
      <>
        In a second terminal, in the same project. Then open{" "}
        <span className="font-mono">http://localhost:7777</span> in your browser and leave it open.
      </>
    ),
    command: "heall serve",
  },
  {
    title: "Run heall when a build is red",
    text: (
      <>
        Replace <span className="font-mono">LAST_GREEN_COMMIT</span> with a commit where your tests passed. The run shows
        up in the dashboard by itself. <span className="font-mono">--dry-run</span> saves the fix as a patch and pushes
        nothing.
      </>
    ),
    command: "heall run --good LAST_GREEN_COMMIT --dry-run",
  },
];

// Every command, grouped by what a user is trying to do. The capitalised
// words are the parts to replace.
const REFERENCE: { group: string; note?: string; commands: { command: string; text: string }[] }[] = [
  {
    group: "Everyday",
    commands: [
      { command: "heall init", text: "Connect the repository you are in: writes .heall.yaml and checks your machine." },
      { command: "heall doctor", text: "Check that Docker, Python, your Groq key and the GitHub CLI are in place." },
      { command: "heall serve", text: "Open the dashboard at http://localhost:7777. Leave it running in its own terminal." },
      {
        command: "heall run --good LAST_GREEN_COMMIT --dry-run",
        text: "Find the culprit and a verified fix. The patch and a report are saved under .heall/; nothing is pushed.",
      },
      {
        command: "heall run --good LAST_GREEN_COMMIT",
        text: "The same, and open a draft pull request with the fix. Needs the repository on GitHub and gh auth login.",
      },
      { command: "heall pr .heall/RUN_ID", text: "Open the pull request for a fix that an earlier dry run saved." },
    ],
  },
  {
    group: "Telling heall where the failure is",
    commands: [
      {
        command: "heall run --good LAST_GREEN_COMMIT --bad BRANCH",
        text: "Heal another branch or commit than the one you are on.",
      },
      { command: "heall run --good LAST_GREEN_COMMIT --log test-output.log", text: "Read the failing test from a saved test log instead of running the suite." },
      { command: "heall run --good LAST_GREEN_COMMIT --run-id RUN_ID", text: "Read it from a failed GitHub Actions run." },
    ],
  },
  {
    group: "One stage at a time",
    note: "heall run does all of these in order. Run one by itself to see or repeat a single stage.",
    commands: [
      { command: "heall triage test-output.log", text: "Which test failed, where, and which files to look at first." },
      { command: 'heall reproduce --good LAST_GREEN_COMMIT --test "TEST NAME"', text: "Is the failure real, or is the test flaky?" },
      { command: 'heall locate --good LAST_GREEN_COMMIT --test "TEST NAME"', text: "Which commit introduced the failure." },
      {
        command: 'heall heal --good LAST_GREEN_COMMIT --test "TEST NAME" --culprit CULPRIT_COMMIT',
        text: "Ask the agent for a fix for a known culprit, and verify it.",
      },
    ],
  },
  {
    group: "Help",
    commands: [
      { command: "heall --help", text: "List every command." },
      { command: "heall run --help", text: "Every option of one command. Works for each command." },
      { command: "heall --version", text: "Which version is installed." },
    ],
  },
];

// A command with a button that copies it. The button says so for a moment,
// in words as well as by its icon.
export function Command({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => () => void (timer.current && clearTimeout(timer.current)), []);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      // Without clipboard access (an old browser, or a page not served over
      // HTTPS) fall back to selecting a hidden copy of the text.
      const area = document.createElement("textarea");
      area.value = text;
      area.style.position = "fixed";
      area.style.opacity = "0";
      document.body.appendChild(area);
      area.select();
      document.execCommand("copy");
      area.remove();
    }
    setCopied(true);
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopied(false), 1800);
  };

  return (
    <div className="flex items-stretch overflow-hidden rounded-md border border-line bg-page">
      <pre className="min-w-0 flex-1 overflow-x-auto px-3 py-2.5 font-mono text-[13px] leading-5 text-ink">
        <span className="text-muted select-none">$ </span>
        {text}
      </pre>
      <button
        type="button"
        onClick={copy}
        aria-label={copied ? "Copied" : `Copy the command: ${text}`}
        title={copied ? "Copied" : "Copy"}
        className="flex shrink-0 items-center gap-1.5 border-l border-line px-3 text-xs text-ink-2 hover:bg-raised hover:text-ink focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent"
      >
        <svg viewBox="0 0 16 16" className="size-4" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
          {copied ? (
            <path d="M3 8.5l3.2 3.2L13 5" />
          ) : (
            <>
              <rect x="5.5" y="5.5" width="8" height="8" rx="1.5" />
              <path d="M10.5 5.5v-2a1 1 0 0 0-1-1h-6a1 1 0 0 0-1 1v6a1 1 0 0 0 1 1h2" />
            </>
          )}
        </svg>
        <span className="hidden w-10 text-left sm:inline">{copied ? "Copied" : "Copy"}</span>
      </button>
    </div>
  );
}

// GetStarted is the page a newcomer lands on: what heall is, and the steps
// to use it on their own repository, each with its command ready to copy.
export function GetStarted({ onDashboard }: { onDashboard: () => void }) {
  return (
    <main className="pb-10">
      <div className="py-6 text-center">
        <h2 className="text-3xl font-semibold tracking-tight">Fix a red build, with proof</h2>
        <p className="mx-auto mt-3 max-w-2xl text-base text-ink-2">
          heall finds the commit that broke your tests, asks a model for a fix, and proves the fix in a sandbox before
          opening a draft pull request. When it cannot prove a fix, it stops and tells you why.
        </p>
        <p className="mt-2 text-sm text-muted">It runs on your machine. Your code is not uploaded anywhere.</p>
      </div>

      <section className="rounded-lg border border-line bg-surface" aria-labelledby="steps">
        <h3 id="steps" className="border-b border-line px-5 py-3 text-sm font-semibold">
          Get started in five steps
        </h3>
        <ol className="space-y-6 p-5">
          {STEPS.map((step, i) => (
            <li key={step.title} className="flex min-w-0 gap-3">
              <span className="grid size-6 shrink-0 place-items-center rounded-full bg-ink text-xs font-bold text-page">{i + 1}</span>
              <div className="min-w-0 flex-1 space-y-2">
                <h4 className="text-sm leading-6 font-semibold">{step.title}</h4>
                <p className="text-sm text-ink-2">{step.text}</p>
                <Command text={step.command} />
              </div>
            </li>
          ))}
        </ol>
        <div className="border-t border-line px-5 py-3 text-xs text-ink-2">
          <span className="font-semibold text-ink">Works today with:</span> JavaScript projects tested with Node&apos;s
          built-in runner (<span className="font-mono">node --test</span>) that have no npm dependencies.
        </div>
      </section>

      <section className="mt-6 rounded-lg border border-line bg-surface" aria-labelledby="good-commit">
        <h3 id="good-commit" className="border-b border-line px-5 py-3 text-sm font-semibold">
          What to put for LAST_GREEN_COMMIT
        </h3>
        <div className="space-y-3 p-5 text-sm text-ink-2">
          <p>
            Any earlier commit where your tests passed. heall searches from there to where you are now. List your
            commits and copy the short code at the start of a line from before the tests broke:
          </p>
          <Command text="git log --oneline" />
          <p>
            A branch name (<span className="font-mono">main</span>), a tag (<span className="font-mono">v1.2.0</span>) or
            a count back from now (<span className="font-mono">HEAD~20</span>) work as well. It need not be the very
            last passing commit: an older one only makes the search a little longer. If the tests fail there too, heall
            says so and changes nothing, and you try an earlier one.
          </p>
        </div>
      </section>

      <section className="mt-6 rounded-lg border border-line bg-surface" aria-labelledby="commands">
        <h3 id="commands" className="border-b border-line px-5 py-3 text-sm font-semibold">
          All commands
        </h3>
        <div className="space-y-6 p-5">
          {REFERENCE.map((group) => (
            <div key={group.group}>
              <h4 className="text-xs font-semibold tracking-wide text-muted uppercase">{group.group}</h4>
              {group.note && <p className="mt-1 text-sm text-ink-2">{group.note}</p>}
              <ul className="mt-2 space-y-3">
                {group.commands.map((c) => (
                  <li key={c.command} className="space-y-1.5">
                    <Command text={c.command} />
                    <p className="text-sm text-ink-2">{c.text}</p>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
        <p className="border-t border-line px-5 py-3 text-xs text-ink-2">
          <span className="font-semibold text-ink">How a command ends:</span> exit code 0 means it succeeded, 3 means
          heall stopped on purpose and explained why, and 1 is an error.
        </p>
      </section>

      <div className="mt-6 flex flex-wrap items-center justify-center gap-x-5 gap-y-3">
        <button type="button" onClick={onDashboard} className="rounded-md bg-ink px-4 py-2 text-sm font-medium text-page hover:opacity-90">
          Open the dashboard
        </button>
        <a href="https://github.com/TheCodeHeist-Coder/heall" target="_blank" rel="noreferrer" className="text-sm text-accent underline-offset-2 hover:underline">
          Source and documentation on GitHub
        </a>
      </div>
    </main>
  );
}
