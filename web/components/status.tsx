import type { Verdict } from "@/lib/events";

// Every verdict has a glyph and a word as well as a colour, so it reads the
// same for someone who cannot tell red from green.
export const VERDICT: Record<Verdict, { glyph: string; label: string; dot: string }> = {
  pass: { glyph: "✓", label: "passed", dot: "bg-good" },
  fail: { glyph: "✕", label: "failed", dot: "bg-bad" },
  flaky: { glyph: "~", label: "flaky", dot: "bg-warn" },
  skipped: { glyph: "–", label: "skipped", dot: "bg-skip" },
};

export function short(sha: string): string {
  return sha.slice(0, 10);
}

export function seconds(ms: number): string {
  return `${(ms / 1000).toFixed(1)}s`;
}

export function Panel({
  title,
  aside,
  children,
  className = "",
}: {
  title: string;
  aside?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <section className={`rounded-lg border border-line bg-surface ${className}`}>
      <header className="flex items-baseline justify-between gap-3 border-b border-line px-4 py-2.5">
        <h2 className="text-sm font-semibold">{title}</h2>
        {aside && <div className="text-xs text-ink-2">{aside}</div>}
      </header>
      {children}
    </section>
  );
}

// A status mark: a coloured dot, a glyph and a word, never colour alone.
export function Mark({ tone, children }: { tone: "good" | "bad" | "warn" | "idle"; children: React.ReactNode }) {
  const style = {
    good: ["bg-good-wash", "bg-good", "✓"],
    bad: ["bg-bad-wash", "bg-bad", "✕"],
    warn: ["bg-warn-wash", "bg-warn", "!"],
    idle: ["bg-raised", "bg-skip", "·"],
  }[tone];
  return (
    <span className={`inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium text-ink ${style[0]}`}>
      <span className={`grid size-3.5 place-items-center rounded-full text-[9px] leading-none font-bold ${style[1]} ${tone === "bad" ? "text-white" : "text-black"}`}>
        {style[2]}
      </span>
      {children}
    </span>
  );
}
