import { useEffect, useState } from "react";
import { MessageList } from "./MailViews";
import type { Conversation, SwipePreferences } from "../../types";
import type { AddToast, DatePrefs } from "../../appTypes";
import { MailSideChat } from "./MailSideChat";


type Message = {
  id: number; provider: string; account: string; native_id: string; thread_id: string;
  folders: string[]; labels: string[]; subject: string; from: string; body: string;
  read: boolean; starred: boolean; oversized: boolean;
  attachments: { id: number; filename: string; size: number }[];
};
type Census = { account: { id: string }; message_count: number; estimated_raw_bytes: number; expected_evidence_bytes: number; recommended_free_disk: number; method: string };
type Data = { messages: Message[]; conversations: Conversation[]; census: Census[]; real_credentials: number; oauth_accounts: number };

export function MockHub({ csrf, datePrefs, swipePreferences, navigate, addToast }: { csrf: string; datePrefs: DatePrefs; swipePreferences: SwipePreferences; navigate: (url: string) => void; addToast: AddToast }) {
  const [data, setData] = useState<Data>({ messages: [], conversations: [], census: [], real_credentials: 0, oauth_accounts: 0 });
  const [view, setView] = useState("inbox");
  const [account, setAccount] = useState("");
  const [provider, setProvider] = useState("mock");
  const [label, setLabel] = useState("");
  const [query, setQuery] = useState("");
  const [search, setSearch] = useState("");
  const [selected, setSelected] = useState<Message | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const [newLabel, setNewLabel] = useState("");

  async function getData(params: URLSearchParams): Promise<Data> {
    const response = await fetch(`/api/mock-hub?${params}`, { cache: "no-store" });
    if (!response.ok) throw new Error(`Mock hub request failed (${response.status})`);
    return response.json();
  }
  useEffect(() => {
    let active = true;
    setError("");
    void getData(new URLSearchParams({ view, provider, account, label, q: search }))
      .then(value => {
        if (!active) return;
        setData(value);
        setSelected(current => (current ? value.messages.find(message => message.id === current.id) : undefined) || value.messages[0] || null);
      })
      .catch(e => { if (active) setError(String(e)); });
    return () => { active = false; };
  }, [view, provider, account, label, search, revision]);

  async function post(path: string, body: unknown) {
    setBusy(true); setError("");
    try {
      const response = await fetch(`/api/mock-hub/${path}`, { method: "POST", headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf }, body: JSON.stringify(body) });
      if (!response.ok) { const value = await response.json().catch(() => null); throw new Error(value?.error || `Request failed (${response.status})`); }
      setRevision(value => value + 1);
    } catch (e) { setError(String(e)); } finally { setBusy(false); }
  }
  function mutate(m: Message, action: string, value = false, label = "") {
    void post("mutate", { account: m.account, mutation: { message_id: m.native_id, action, value, label } });
  }

  return <section className="mock-hub" aria-label="Mock Mail Hub">
    <h1>Mock Mail Hub</h1>
    <p>Fixed synthetic mail. Real credentials: {data.real_credentials} · OAuth: {data.oauth_accounts}</p>
    <button disabled={busy} onClick={() => void post("sync", {})}>Sync MockProvider</button>
    <nav aria-label="Unified mail views">
      {[["inbox", "Unified Inbox"], ["all", "All Mail"], ["unread", "Unread"], ["starred", "Starred"], ["trash", "Trash"]].map(([id, title]) =>
        <button key={id} aria-pressed={view === id} onClick={() => setView(id)}>{title}</button>)}
    </nav>
    <form onSubmit={event => { event.preventDefault(); setSearch(query); }}>
      <label>Provider <select value={provider} onChange={e => setProvider(e.target.value)}><option value="">All providers</option><option value="mock">Mock</option></select></label>
      <label>Account <select value={account} onChange={e => setAccount(e.target.value)}><option value="">All accounts</option><option>personal</option><option>work</option></select></label>
      <label>Label / folder <input value={label} onChange={e => setLabel(e.target.value)} placeholder="项目, important, Documents, INBOX" /></label>
      <label>Search <input value={query} onChange={e => setQuery(e.target.value)} placeholder="Subject, body, Message-ID, attachment" /></label>
      <button type="submit">Search</button>
    </form>
    {error && <p role="alert">{error}</p>}
    <p>{data.messages.length} messages</p>
    <MessageList csrf={csrf} conversations={data.conversations || []} hiddenMessageIDs={new Set()} mailboxes={[]} swipePreferences={swipePreferences} datePrefs={datePrefs} returnURL="/mock" navigate={navigate} addToast={addToast} searchQuery={search} onStarredChange={() => setRevision(value => value + 1)} onReadStatesChange={() => setRevision(value => value + 1)} onMessagesMoved={() => setRevision(value => value + 1)} />
    <h2>Source actions</h2>
    <ul>{data.messages.map(m => <li key={m.id}>
      <button onClick={() => setSelected(m)}>{m.starred ? "★ " : ""}{m.read ? "" : "● "}{m.subject}</button>
      <span> {m.provider} / {m.account} · {m.folders.join(", ")} · {m.labels.join(", ")}{m.oversized ? " · Oversized stub" : ""}</span>
    </li>)}</ul>
    {selected && <section aria-label="Mock source actions">
      <h2>Source · {selected.account} / {selected.thread_id}</h2>
      <label>New label <input value={newLabel} onChange={e => setNewLabel(e.target.value)} /></label>
      {data.messages.filter(m => m.id === selected.id).map(m => <article key={m.id}>
        <h3>{m.subject}</h3><p>{m.from} · Mock / {m.account} · {m.native_id}</p>
        <p>{m.folders.join(", ")} · {m.labels.join(", ")}</p>
        <button disabled={busy} onClick={() => mutate(m, "read", !m.read)}>{m.read ? "Mark unread" : "Mark read"}</button>
        <button disabled={busy} onClick={() => mutate(m, "star", !m.starred)}>{m.starred ? "Unstar" : "Star"}</button>
        <button disabled={busy || !newLabel.trim()} onClick={() => mutate(m, "label", true, newLabel)}>Add label</button>
        {m.labels.map(name => <button key={name} disabled={busy} onClick={() => mutate(m, "label", false, name)}>Remove {name}</button>)}
        <button disabled={busy} onClick={() => mutate(m, "archive")}>Archive</button>
        <button disabled={busy} onClick={() => { if (window.confirm("Move this synthetic message to Mock Trash?")) mutate(m, "trash"); }}>Trash</button>
        <button onClick={() => navigate(`/messages/${m.id}?return=${encodeURIComponent("/mock")}`)}>Open thread and attachments</button>
      </article>)}
    </section>}
    {selected ? <MailSideChat csrf={csrf} messageID={selected.id} defaultOpen /> : null}
    <section aria-label="Storage Census"><h2>Storage Census</h2>
      <p>Synthetic metadata before raw ingestion. Evidence bytes are a capacity estimate. Disk budget: 1.5 × raw bytes.</p>
      <table><thead><tr><th>Account</th><th>Messages</th><th>Estimated raw bytes</th><th>Expected Evidence bytes</th><th>Recommended free disk</th></tr></thead>
        <tbody>{data.census.map(c => <tr key={c.account.id}><td>Mock / {c.account.id}</td><td>{c.message_count}</td><td>{c.estimated_raw_bytes}</td><td>{c.expected_evidence_bytes}</td><td>{c.recommended_free_disk}</td></tr>)}</tbody></table>
    </section>
  </section>;
}
