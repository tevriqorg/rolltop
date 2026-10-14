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
type ModelConfig = { available?: boolean; configured: boolean; can_configure?: boolean; base_url?: string; model?: string; api_key_configured?: boolean };

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

export function MailSideChat({ csrf, messageID, defaultOpen = false }: { csrf: string; messageID: number; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(defaultOpen);
  const [modelConfig, setModelConfig] = useState<ModelConfig | null>(null);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [baseURL, setBaseURL] = useState("");
  const [model, setModel] = useState("");
  const [apiKey, setAPIKey] = useState("");
  const [clearAPIKey, setClearAPIKey] = useState(false);
  const [configBusy, setConfigBusy] = useState(false);
  const [prompt, setPrompt] = useState("");
  const [busy, setBusy] = useState(false);
  const [lines, setLines] = useState<ChatLine[]>([]);
  const [trace, setTrace] = useState<TraceLine[]>([]);
  const [pending, setPending] = useState<PendingAction[]>([]);
  const seq = useRef(1);

  useEffect(() => {
    let active = true;
    void fetch("/api/mail-ai/model-config", { headers: { Accept: "application/json" }, cache: "no-store" })
      .then(async (response): Promise<ModelConfig> => response.ok ? await response.json() as ModelConfig : { available: false, configured: false })
      .then((value) => {
        if (!active) return;
        setModelConfig(value);
        setBaseURL(value.base_url || "");
        setModel(value.model || "");
      })
      .catch(() => { if (active) setModelConfig({ available: false, configured: false, can_configure: false }); });
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

  async function saveModelConfig() {
    if (configBusy) return;
    const cleanBaseURL = baseURL.trim();
    const cleanModel = model.trim();
    if (!cleanBaseURL || !cleanModel) {
      pushTrace("Base URL and model are required.", "error");
      return;
    }
    setConfigBusy(true);
    try {
      const body: Record<string, unknown> = { base_url: cleanBaseURL, model: cleanModel };
      if (apiKey.trim()) body.api_key = apiKey.trim();
      else if (clearAPIKey) body.clear_api_key = true;
      const response = await fetch("/api/mail-ai/model-config", {
        method: "PUT",
        headers: { Accept: "application/json", "Content-Type": "application/json", "X-CSRF-Token": csrf },
        body: JSON.stringify(body)
      });
      const value = await response.json().catch(() => ({})) as ModelConfig & { error?: string };
      if (!response.ok) throw new Error(value.error || `Model settings failed (${response.status})`);
      setModelConfig({ ...value, available: true });
      setBaseURL(value.base_url || cleanBaseURL);
      setModel(value.model || cleanModel);
      setAPIKey("");
      setClearAPIKey(false);
      setSettingsOpen(false);
      pushTrace(`Model configured ✓ ${value.model || cleanModel}`);
    } catch (error) {
      pushTrace(error instanceof Error ? error.message : String(error), "error");
    } finally {
      setConfigBusy(false);
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

  const configured = Boolean(modelConfig?.configured);
  const statusText = modelConfig === null
    ? "Checking…"
    : modelConfig.available === false
      ? "Unavailable"
      : configured
        ? (modelConfig.model || "Ready")
        : "Model not configured";

  if (!open) {
    return <button className="mail-ai-launch" type="button" onClick={() => setOpen(true)} disabled={messageID <= 0} title="Open Mail AI Side Chat">AI</button>;
  }
  return (
    <aside className="mail-ai-panel" aria-label="Mail AI Side Chat">
      <header>
        <div><strong>Mail AI</strong><span>{statusText}</span></div>
        <div className="mail-ai-header-actions">
          {modelConfig?.can_configure ? <button className="ghost" type="button" onClick={() => setSettingsOpen((value) => !value)} aria-expanded={settingsOpen}>Model</button> : null}
          <button className="ghost" type="button" onClick={() => setOpen(false)} aria-label="Close Mail AI">×</button>
        </div>
      </header>
      {settingsOpen ? <section className="mail-ai-settings" aria-label="Mail AI model settings">
        <form onSubmit={(event) => { event.preventDefault(); void saveModelConfig(); }}>
          <label>Base URL<input type="url" value={baseURL} onChange={(event) => setBaseURL(event.target.value)} placeholder="https://api.example.com/v1" autoComplete="off" /></label>
          <label>Model<input value={model} onChange={(event) => setModel(event.target.value)} placeholder="model-name" autoComplete="off" /></label>
          <label>API key<input type="password" value={apiKey} onChange={(event) => setAPIKey(event.target.value)} placeholder={modelConfig?.api_key_configured ? "Configured — leave blank to keep" : "Optional for keyless endpoints"} autoComplete="off" /></label>
          {modelConfig?.api_key_configured ? <label className="mail-ai-clear-key"><input type="checkbox" checked={clearAPIKey} onChange={(event) => setClearAPIKey(event.target.checked)} />Clear saved API key</label> : null}
          <small>The API key is stored by Mail AI on the server and is never returned to the browser.</small>
          <button type="submit" disabled={configBusy || !baseURL.trim() || !model.trim()}>{configBusy ? "Saving…" : "Save model"}</button>
        </form>
      </section> : null}
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
        <textarea value={prompt} onChange={(event) => setPrompt(event.target.value)} placeholder={configured ? "Ask about this message…" : "Configure a model first…"} rows={3} disabled={busy || !configured} />
        <button type="submit" disabled={busy || !configured || !prompt.trim()}>{busy ? "Working…" : "Send"}</button>
      </form>
    </aside>
  );

}
