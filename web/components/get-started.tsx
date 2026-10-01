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

// A command with a button that copies it. The button says so for a moment,
// in words as well as by its icon.
function Command({ text }: { text: string }) {
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

// GetStarted tells a newcomer what heall is and how to use it on their own
// repository. It is what the public site opens with.
export function GetStarted({ onClose }: { onClose?: () => void }) {
  return (
    <section className="rounded-lg border border-line bg-surface" aria-labelledby="get-started">
      <div className="flex items-start gap-4 border-b border-line px-5 py-4">
        <div className="min-w-0 flex-1">
          <h2 id="get-started" className="text-xl font-semibold tracking-tight">
            Fix a red build, with proof
          </h2>
          <p className="mt-1 max-w-3xl text-sm text-ink-2">
            heall finds the commit that broke your tests, asks a model for a fix, and proves the fix in a sandbox before
            opening a draft pull request. When it cannot prove a fix, it stops and tells you why. It runs on your
            machine; your code is not uploaded anywhere.
          </p>
        </div>
        {onClose && (
          <button type="button" onClick={onClose} className="shrink-0 rounded-md border border-line px-2.5 py-1.5 text-sm hover:bg-raised">
            Hide
          </button>
        )}
      </div>

      <ol className="grid grid-cols-[minmax(0,1fr)] gap-x-6 gap-y-5 p-5 lg:grid-cols-[repeat(2,minmax(0,1fr))]">
        {STEPS.map((step, i) => (
          <li key={step.title} className={`flex min-w-0 gap-3 ${i === STEPS.length - 1 ? "lg:col-span-2" : ""}`}>
            <span className="grid size-6 shrink-0 place-items-center rounded-full bg-ink text-xs font-bold text-page">{i + 1}</span>
            <div className="min-w-0 flex-1 space-y-2">
              <h3 className="text-sm leading-6 font-semibold">{step.title}</h3>
              <p className="text-sm text-ink-2">{step.text}</p>
              <Command text={step.command} />
            </div>
          </li>
        ))}
      </ol>

      <div className="flex flex-wrap items-center gap-x-6 gap-y-2 border-t border-line px-5 py-3 text-xs text-ink-2">
        <span>
          <span className="font-semibold text-ink">Works today with:</span> JavaScript projects tested with Node&apos;s
          built-in runner (<span className="font-mono">node --test</span>) that have no npm dependencies.
        </span>
        <a href="https://github.com/TheCodeHeist-Coder/heall" target="_blank" rel="noreferrer" className="text-accent underline-offset-2 hover:underline">
          Source and documentation on GitHub
        </a>
      </div>
    </section>
  );
}
