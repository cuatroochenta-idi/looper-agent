import { createMemo, For } from "solid-js";
import { cn } from "../ui/cn";
import { prettyJson } from "../../lib/format";

type Tok = { text: string; cls: string };

// Minimal, safe highlighter over pretty-printed JSON lines. Never renders raw
// HTML — tokens are emitted as spans, so untrusted tool output can't inject.
const KEY = "text-accent";
const STR = "text-success";
const NUM = "text-warning";
const BOOL = "text-danger";
const PUNC = "text-faint";
const PLAIN = "text-muted";

function tokenizeLine(line: string): Tok[] {
  const toks: Tok[] = [];
  const re = /("(?:[^"\\]|\\.)*"\s*:)|("(?:[^"\\]|\\.)*")|(-?\d+\.?\d*(?:[eE][+-]?\d+)?)|(true|false|null)|([{}[\],])|(\s+)/g;
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(line))) {
    if (m.index > last) toks.push({ text: line.slice(last, m.index), cls: PLAIN });
    if (m[1]) toks.push({ text: m[1], cls: KEY });
    else if (m[2]) toks.push({ text: m[2], cls: STR });
    else if (m[3]) toks.push({ text: m[3], cls: NUM });
    else if (m[4]) toks.push({ text: m[4], cls: BOOL });
    else if (m[5]) toks.push({ text: m[5], cls: PUNC });
    else if (m[6]) toks.push({ text: m[6], cls: PLAIN });
    last = re.lastIndex;
  }
  if (last < line.length) toks.push({ text: line.slice(last), cls: PLAIN });
  return toks;
}

export function JsonViewer(props: { raw: string; class?: string; maxHeight?: string }) {
  const lines = createMemo(() => prettyJson(props.raw).split("\n"));
  return (
    <pre
      class={cn(
        "overflow-auto rounded-[8px] border border-line bg-sunken px-3 py-2 font-mono text-[11.5px] leading-[1.55]",
        props.class,
      )}
      style={{ "max-height": props.maxHeight ?? "340px" }}
    >
      <code>
        <For each={lines()}>
          {(line) => (
            <div class="whitespace-pre">
              <For each={tokenizeLine(line)}>{(t) => <span class={t.cls}>{t.text}</span>}</For>
            </div>
          )}
        </For>
      </code>
    </pre>
  );
}
