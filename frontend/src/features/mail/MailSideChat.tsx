import { useEffect, useRef, useState } from "react";

export type MailAIEvent = {
  type: string;
  text?: string;
  tool?: string;
  arguments?: unknown;
  result?: unknown;
  action_token?: string;
};

type ChatLine = { id: number; role: "user" | "assistant"; text: string };
type TraceLine = { id: number; text: string; kind: "tool" | "status" | "error" };
type PendingAction = { id: number; tool: string; token: string; args: unknown; busy: boolean };

export function parseMailAIFrames(buffer: string): { events: MailAIEvent[]; rest: string } {
  const normalized = buffer.replace(/\r\n/g, "\n");
  const blocks = normalized.split("\n\n");
  const rest = blocks.pop() || "";
  const events: MailAIEvent[] = [];
  for (const block of blocks) {
    const payload = block.split("\n")
      .filter((line) => line.startsWith("data:"))
      .map((line) => line.slice(5).trimStart())
      .join("\n");
    if (!payload) continue;
    try { events.push(JSON.parse(payload) as MailAIEvent); } catch { /* ignore malformed sidecar frame */ }
  }
  return { events, rest };
}

async function consumeMailAIStream(response: Response, onEvent: (event: MailAIEvent) => void) {
  if (!response.ok) {
    const body = await response.json().catch(() => null) as { error?: string } | null;
    throw new Error(body?.error || `Mail AI request failed (${response.status})`);
  }
  if (!response.body) throw new Error("Mail AI stream is unavailable");
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  for (;;) {
    const { done, value } = await reader.read();
    buffer += decoder.decode(value || new Uint8Array(), { stream: !done });
    const parsed = parseMailAIFrames(buffer);
    buffer = parsed.rest;
    parsed.events.forEach(onEvent);
    if (done) break;
  }
  const parsed = parseMailAIFrames(buffer + "\n\n");
  parsed.events.forEach(onEvent);
}

function compact(value: unknown) {
  if (typeof value === "string") return value;
  try { return JSON.stringify(value); } catch { return String(value); }
}

export function MailSideChat({ csrf, messageID }: { csrf: string; messageID: number }) {
  const [open, setOpen] = useState(false);
  const [configured, setConfigured] = useState<boolean | null>(null);
  const [prompt, setPrompt] = useState("");
  const [busy, setBusy] = useState(false);
  const [lines, setLines] = useState<ChatLine[]>([]);
  const [trace, setTrace] = useState<TraceLine[]>([]);
  const [pending, setPending] = useState<PendingAction[]>([]);
  const seq = useRef(1);

  useEffect(() => {
    let active = true;
    void fetch("/api/mail-ai/status", { headers: { Accept: "application/json" }, cache: "no-store" })
      .then(async (response) => response.ok ? response.json() as Promise<{ configured: boolean }> : { configured: false })
      .then((value) => { if (active) setConfigured(Boolean(value.configured)); })
      .catch(() => { if (active) setConfigured(false); });
    return () => { active = false; };
  }, []);

  function pushTrace(text: string, kind: TraceLine["kind"] = "status") {
    setTrace((current) => [...current, { id: seq.current++, text, kind }].slice(-16));
  }

  function onEvent(event: MailAIEvent, assistantID: number) {
    switch (event.type) {
      case "context":
        pushTrace("Current mail context attached by Mail Hub.");
        break;
      case "tool_call":
        pushTrace(`Tool → ${event.tool || "unknown"} ${compact(event.arguments || {})}`, "tool");
        break;
      case "tool_result":
        pushTrace(`Tool ✓ ${event.tool || "unknown"}`, "tool");
        break;
      case "confirmation_required":
        if (event.action_token && event.tool) {
          const id = seq.current++;
          setPending((current) => [...current, { id, tool: event.tool || "action", token: event.action_token || "", args: event.arguments, busy: false }]);
          pushTrace(`Confirmation required → ${event.tool}`, "status");
        }
        break;
      case "answer_delta":
        if (event.text) setLines((current) => current.map((line) => line.id === assistantID ? { ...line, text: line.text + event.text } : line));
        break;
      case "error":
        pushTrace(event.text || "Mail AI error", "error");
        break;
      default:
        break;
    }
  }

  async function send() {
    const text = prompt.trim();
    if (!text || busy || messageID <= 0) return;
    const userID = seq.current++;
    const assistantID = seq.current++;
    setLines((current) => [...current, { id: userID, role: "user", text }, { id: assistantID, role: "assistant", text: "" }]);
    setPrompt("");
    setBusy(true);
    try {
      const response = await fetch("/api/mail-ai/chat", {
        method: "POST",
        headers: { Accept: "text/event-stream", "Content-Type": "application/json", "X-CSRF-Token": csrf },
        body: JSON.stringify({ message_id: messageID, prompt: text })
      });
      await consumeMailAIStream(response, (event) => onEvent(event, assistantID));
    } catch (error) {
      pushTrace(error instanceof Error ? error.message : String(error), "error");
    } finally {
      setBusy(false);
    }
  }

  async function confirm(action: PendingAction) {
    setPending((current) => current.map((item) => item.id === action.id ? { ...item, busy: true } : item));
    try {
      const response = await fetch("/api/mail-ai/confirm", {
        method: "POST",
        headers: { Accept: "application/json", "Content-Type": "application/json", "X-CSRF-Token": csrf },
        body: JSON.stringify({ action_token: action.token })
      });
      const value = await response.json().catch(() => ({})) as { error?: string; result?: unknown };
      if (!response.ok) throw new Error(value.error || `Confirmation failed (${response.status})`);
      pushTrace(`Confirmed ✓ ${action.tool}`, "tool");
      setPending((current) => current.filter((item) => item.id !== action.id));
    } catch (error) {
      pushTrace(error instanceof Error ? error.message : String(error), "error");
      setPending((current) => current.map((item) => item.id === action.id ? { ...item, busy: false } : item));
    }
  }

  if (!open) {
    return <button className="mail-ai-launch" type="button" onClick={() => setOpen(true)} disabled={messageID <= 0} title="Open Mail AI Side Chat">AI</button>;
  }
  return (
    <aside className="mail-ai-panel" aria-label="Mail AI Side Chat">
      <header>
        <div><strong>Mail AI</strong><span>{configured === false ? "Not configured" : configured === true ? "Mock mail context" : "Checking…"}</span></div>
        <button className="ghost" type="button" onClick={() => setOpen(false)} aria-label="Close Mail AI">×</button>
      </header>
      <div className="mail-ai-transcript" aria-live="polite">
        {lines.length === 0 ? <p className="muted">Ask about this mail, search history, or draft a reply. Drafts are never sent automatically.</p> : null}
        {lines.map((line) => <div className={`mail-ai-line ${line.role}`} key={line.id}><strong>{line.role === "user" ? "You" : "AI"}</strong><p>{line.text || (busy ? "…" : "")}</p></div>)}
      </div>
      {trace.length > 0 ? <details className="mail-ai-trace" open><summary>Tool activity</summary>{trace.map((item) => <p className={item.kind} key={item.id}>{item.text}</p>)}</details> : null}
      {pending.map((action) => <section className="mail-ai-confirm" key={action.id} aria-label={`Confirm ${action.tool}`}>
        <strong>Confirm action: {action.tool}</strong><code>{compact(action.args)}</code>
        <div><button type="button" disabled={action.busy} onClick={() => void confirm(action)}>Confirm</button><button type="button" className="secondary" onClick={() => setPending((current) => current.filter((item) => item.id !== action.id))}>Cancel</button></div>
      </section>)}
      <form onSubmit={(event) => { event.preventDefault(); void send(); }}>
        <textarea value={prompt} onChange={(event) => setPrompt(event.target.value)} placeholder="Ask about this message…" rows={3} disabled={busy || configured === false} />
        <button type="submit" disabled={busy || configured === false || !prompt.trim()}>{busy ? "Working…" : "Send"}</button>
      </form>
    </aside>
  );
}
