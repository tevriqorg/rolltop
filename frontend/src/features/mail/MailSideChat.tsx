import { useCallback, useEffect, useMemo, useState } from "react";
import {
  ActionBarPrimitive,
  AssistantRuntimeProvider,
  ComposerPrimitive,
  MessagePrimitive,
  SuggestionPrimitive,
  ThreadPrimitive,
  createSuggestionAdapter,
  type TextMessagePartProps,
  type ToolCallMessagePartProps
} from "@assistant-ui/react";
import { AssistantChatTransport, useChatRuntime } from "@assistant-ui/ai-sdk";
import { lastAssistantMessageIsCompleteWithApprovalResponses, type UIMessage } from "ai";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

type ModelConfig = { configured: boolean; model?: string };
type ThreadScope = "global" | "message" | "mail_thread";
type ThreadContext = {
  subject?: string;
  account?: string;
  message_id?: number;
  mail_thread_id?: string;
};
type ThreadSnapshot = {
  thread_id: string;
  scope: ThreadScope;
  context: ThreadContext;
  preview: string;
  message_count: number;
  messages: UIMessage[];
  created_at: string;
  updated_at: string;
};
type ThreadSummary = Omit<ThreadSnapshot, "messages">;

type ThreadListResponse = { threads: ThreadSummary[] };
type TraceEvent = {
  id: string;
  message_id: string;
  tool_call_id: string;
  tool_name: string;
  state: string;
  input?: unknown;
  output?: unknown;
  error_text?: string;
};
type TraceResponse = { thread_id: string; events: TraceEvent[] };
type TimelinePhase = "浏览" | "搜索" | "读取" | "比较" | "动作" | "结论";
type TimelineItem = {
  id: string;
  phase: TimelinePhase;
  title: string;
  detail: string;
  state: "running" | "done" | "error";
};

const TOOL_LABELS: Record<string, string> = {
  list_mail: "浏览邮箱",
  search_mail: "搜索邮件",
  get_message: "读取邮件",
  get_thread: "读取邮件线程",
  draft_reply: "生成回复草稿",
  add_label: "添加标签",
  archive_message: "归档邮件",
  mark_read: "修改已读状态"
};

export function mailAIScopeLabel(scope: ThreadScope) {
  if (scope === "message") return "当前邮件";
  if (scope === "mail_thread") return "邮件线程";
  return "全部邮箱";
}

export function mailAIWorkspaceInitialScope(messageID: number): ThreadScope {
  return messageID > 0 ? "mail_thread" : "global";
}

export function mailAIWorkspaceAvailableScopes(messageID: number): ThreadScope[] {
  return messageID > 0 ? ["mail_thread", "message", "global"] : ["global"];
}

export function toolActivityLabel(name: string, args: unknown, result: unknown, running: boolean) {
  const label = TOOL_LABELS[name] || name;
  const record = (args && typeof args === "object") ? args as Record<string, unknown> : {};
  if (name === "search_mail" && typeof record.q === "string") return `${running ? "正在搜索" : "已搜索"}：${record.q}`;
  if (running) return `正在${label}`;
  if (Array.isArray(result)) return `${label}完成 · ${result.length} 条结果`;
  return `${label}完成`;
}

type MailToolCardKind = "mail-list" | "message" | "thread" | "draft" | "mutation" | "generic";

type MailToolItem = {
  message_id?: number;
  account?: string;
  thread_id?: string;
  subject?: string;
  from?: string;
  to?: string;
  date?: string;
  body?: string;
};

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

function asMailToolItem(value: unknown): MailToolItem {
  const record = asRecord(value);
  return {
    message_id: typeof record.message_id === "number" ? record.message_id : undefined,
    account: typeof record.account === "string" ? record.account : undefined,
    thread_id: typeof record.thread_id === "string" ? record.thread_id : undefined,
    subject: typeof record.subject === "string" ? record.subject : undefined,
    from: typeof record.from === "string" ? record.from : undefined,
    to: typeof record.to === "string" ? record.to : undefined,
    date: typeof record.date === "string" ? record.date : undefined,
    body: typeof record.body === "string" ? record.body : undefined
  };
}

export function mailToolCardKind(toolName: string): MailToolCardKind {
  if (toolName === "list_mail" || toolName === "search_mail") return "mail-list";
  if (toolName === "get_message") return "message";
  if (toolName === "get_thread") return "thread";
  if (toolName === "draft_reply") return "draft";
  if (toolName === "add_label" || toolName === "archive_message" || toolName === "mark_read") return "mutation";
  return "generic";
}

export function mailToolCardSummary(toolName: string, result: unknown) {
  const kind = mailToolCardKind(toolName);
  if ((kind === "mail-list" || kind === "thread") && Array.isArray(result)) return `${result.length} 封邮件`;
  if (kind === "message") return asMailToolItem(result).subject || "邮件详情";
  if (kind === "draft") {
    const record = asRecord(result);
    return typeof record.subject === "string" && record.subject.trim() ? record.subject : "回复草稿";
  }
  if (kind === "mutation") {
    const record = asRecord(result);
    return record.ok === true ? "已完成" : "等待执行";
  }
  return "工具结果";
}

function compactToolText(value: string | undefined, limit = 180) {
  const text = (value || "").replace(/\s+/g, " ").trim();
  if (!text) return "";
  return text.length > limit ? `${text.slice(0, limit)}…` : text;
}

function MailResultList({ result }: { result: unknown }) {
  const items = Array.isArray(result) ? result.map(asMailToolItem) : [];
  if (items.length === 0) return <div className="mail-ai-tool-empty">没有匹配邮件。</div>;
  return <div className="mail-ai-tool-mail-list">{items.map((item, index) => <article className="mail-ai-tool-mail" key={`${item.message_id || item.thread_id || index}`}>
    <strong>{item.subject || "（无主题）"}</strong>
    <span>{[item.from, item.account, item.date].filter(Boolean).join(" · ")}</span>
    {item.body ? <p>{compactToolText(item.body, 120)}</p> : null}
  </article>)}</div>;
}

function MailMessageCard({ result }: { result: unknown }) {
  const item = asMailToolItem(result);
  return <article className="mail-ai-tool-message">
    <strong>{item.subject || "（无主题）"}</strong>
    <dl>
      {item.from ? <><dt>发件人</dt><dd>{item.from}</dd></> : null}
      {item.to ? <><dt>收件人</dt><dd>{item.to}</dd></> : null}
      {item.account ? <><dt>账号</dt><dd>{item.account}</dd></> : null}
      {item.date ? <><dt>时间</dt><dd>{item.date}</dd></> : null}
    </dl>
    {item.body ? <p>{compactToolText(item.body)}</p> : null}
  </article>;
}

function MailThreadCard({ result }: { result: unknown }) {
  const items = Array.isArray(result) ? result.map(asMailToolItem) : [];
  return <div className="mail-ai-tool-thread">
    <div className="mail-ai-tool-thread-head"><strong>{items[0]?.subject || "邮件线程"}</strong><span>{items.length} 封</span></div>
    {items.map((item, index) => <article key={`${item.message_id || index}`}><span>{item.from || "未知发件人"}</span><time>{item.date || ""}</time>{item.body ? <p>{compactToolText(item.body, 120)}</p> : null}</article>)}
  </div>;
}

function DraftCard({ result }: { result: unknown }) {
  const record = asRecord(result);
  const to = typeof record.to === "string" ? record.to : "";
  const subject = typeof record.subject === "string" ? record.subject : "回复草稿";
  const body = typeof record.body === "string" ? record.body : "";
  return <article className="mail-ai-tool-draft">
    <span className="mail-ai-tool-badge">草稿 · 未发送</span>
    <strong>{subject}</strong>
    {to ? <span>收件人：{to}</span> : null}
    {body ? <p>{body}</p> : null}
  </article>;
}

function MutationCard({ toolName, args, result }: { toolName: string; args: unknown; result: unknown }) {
  const input = asRecord(args);
  const output = asRecord(result);
  const detail = toolName === "add_label" ? `标签：${String(input.label || "")}` : toolName === "mark_read" ? (input.read === false ? "标记为未读" : "标记为已读") : "移出收件箱";
  return <div className="mail-ai-tool-mutation"><strong>{TOOL_LABELS[toolName] || toolName}</strong><span>{detail}</span><span className={output.ok === true ? "done" : "pending"}>{output.ok === true ? "已完成" : "等待执行"}</span></div>;
}

export function mailAITimelinePhase(toolName: string): Exclude<TimelinePhase, "比较" | "结论"> {
  if (toolName === "list_mail") return "浏览";
  if (toolName === "search_mail") return "搜索";
  if (toolName === "get_message" || toolName === "get_thread") return "读取";
  return "动作";
}

function timelineEvidenceIDs(event: TraceEvent) {
  if (event.tool_name !== "get_message" && event.tool_name !== "get_thread") return [] as string[];
  const values = Array.isArray(event.output) ? event.output : event.output ? [event.output] : [];
  return values.map((value) => {
    const record = asRecord(value);
    const messageID = record.message_id;
    if (typeof messageID === "number" || typeof messageID === "string") return String(messageID);
    const threadID = record.thread_id;
    return typeof threadID === "string" ? `thread:${threadID}` : "";
  }).filter(Boolean);
}

export function mailAIFollowUpPrompts(scope: ThreadScope, transcript: string) {
  const text = transcript.toLowerCase();
  const prompts: string[] = [];
  const add = (value: string) => { if (!prompts.includes(value)) prompts.push(value); };

  if (/草稿|回复|draft|reply/.test(text)) {
    add("把这封回复改得更简洁、明确，并保留关键事实");
    add("检查这封回复有没有遗漏需要回应的问题或承诺");
  }
  if (/截止|待办|风险|deadline|action item|risk/.test(text)) {
    add("按优先级整理待办，并标出最晚处理时间");
    add("继续查相关邮件，看看有没有冲突、变更或遗漏");
  }
  if (/搜索|找到|邮件|search|found|message/.test(text)) {
    add("打开最相关的邮件线程，整理关键事实和下一步");
  }

  if (scope === "global") {
    add("找出现在最需要我回复的邮件，并按优先级排序");
    add("汇总未来 7 天的截止事项、负责人和风险");
    add("检查不同邮箱里有没有相互冲突的信息或安排");
  } else {
    add("把当前线程的待办、负责人和截止时间整理成清单");
    add("结合其他邮箱里的相关邮件，检查有没有冲突或遗漏");
    add("根据当前线程帮我起草一封下一步回复");
  }
  return prompts.slice(0, 3);
}

export function latestAssistantVisibleText(messages: UIMessage[]) {
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index];
    if (message.role !== "assistant" || !Array.isArray(message.parts)) continue;
    const text = message.parts.map((part) => {
      const record = part as unknown as Record<string, unknown>;
      return record.type === "text" && typeof record.text === "string" ? record.text.trim() : "";
    }).filter(Boolean).join("\n").trim();
    if (text) return text;
  }
  return "";
}

export function mailAIWorkflowTimeline(events: TraceEvent[], messages: UIMessage[]): TimelineItem[] {
  const items: TimelineItem[] = events.map((event, index) => ({
    id: event.id || `tool:${index}`,
    phase: mailAITimelinePhase(event.tool_name),
    title: toolActivityLabel(event.tool_name, event.input, event.output, event.state !== "output-available" && !event.error_text),
    detail: traceResultSummary(event),
    state: event.error_text ? "error" : event.state === "output-available" ? "done" : "running"
  }));
  const evidence = new Set(events.flatMap(timelineEvidenceIDs));
  if (evidence.size >= 2) {
    const lastRead = items.map((item, index) => item.phase === "读取" ? index : -1).filter((index) => index >= 0).at(-1) ?? items.length - 1;
    items.splice(lastRead + 1, 0, {
      id: "compare:evidence",
      phase: "比较",
      title: `比较 ${evidence.size} 个已读取邮件证据`,
      detail: "基于工具返回的邮件内容汇总证据；不展示模型私有推理。",
      state: "done"
    });
  }
  const conclusion = latestAssistantVisibleText(messages);
  if (conclusion) {
    items.push({
      id: "conclusion:visible",
      phase: "结论",
      title: "最终可见回答",
      detail: compactToolText(conclusion, 220),
      state: "done"
    });
  }
  return items;
}

export function traceResultSummary(event: TraceEvent) {
  if (event.error_text) return `失败：${event.error_text}`;
  if (Array.isArray(event.output)) return `结果：${event.output.length} 条`;
  if (event.output && typeof event.output === "object") {
    const output = event.output as Record<string, unknown>;
    if (typeof output.subject === "string" && output.subject.trim()) return `结果：${output.subject}`;
    if (typeof output.ok === "boolean") return output.ok ? "结果：完成" : "结果：未完成";
    return `结果：${Object.keys(output).slice(0, 4).join("、") || "已返回"}`;
  }
  if (typeof event.output === "string" && event.output.trim()) {
    const text = event.output.trim();
    return `结果：${text.length > 96 ? `${text.slice(0, 96)}…` : text}`;
  }
  return event.state === "output-available" ? "结果：完成" : "等待结果";
}

function UserTextPart({ text }: TextMessagePartProps) {
  return <p className="mail-ai-text">{text}</p>;
}

function AssistantMarkdownPart({ text }: TextMessagePartProps) {
  return <div className="mail-ai-markdown">
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      components={{
        a: ({ href, children }) => <a href={href} target="_blank" rel="noreferrer">{children}</a>
      }}
    >{text}</ReactMarkdown>
  </div>;
}

function ToolCard(props: ToolCallMessagePartProps) {
  const { toolName, args, result, status, approval, respondToApproval, isError } = props;
  const running = status?.type === "running" || result === undefined;
  const pending = approval && approval.approved === undefined && !approval.resolution;
  const kind = mailToolCardKind(toolName);
  const input = asRecord(args);
  const approvalDetail = toolName === "add_label" ? `添加标签 ${String(input.label || "")}` : toolName === "mark_read" ? (input.read === false ? "标记为未读" : "标记为已读") : toolName === "archive_message" ? "归档邮件" : TOOL_LABELS[toolName] || toolName;
  return <section className={`mail-ai-tool-card kind-${kind}${isError ? " error" : ""}`}>
    <div className="mail-ai-tool-title">
      <span className="mail-ai-tool-dot" aria-hidden="true" />
      <strong>{toolActivityLabel(toolName, args, result, running)}</strong>
      {!running && result !== undefined ? <span className="mail-ai-tool-summary">{mailToolCardSummary(toolName, result)}</span> : null}
    </div>
    {!running && kind === "mail-list" ? <MailResultList result={result} /> : null}
    {!running && kind === "message" ? <MailMessageCard result={result} /> : null}
    {!running && kind === "thread" ? <MailThreadCard result={result} /> : null}
    {!running && kind === "draft" ? <DraftCard result={result} /> : null}
    {!running && kind === "mutation" ? <MutationCard toolName={toolName} args={args} result={result} /> : null}
    {pending ? <div className="mail-ai-approval">
      <div><strong>需要确认</strong><span>{approvalDetail}</span>{typeof input.message_id === "number" ? <small>邮件 #{input.message_id}</small> : null}</div>
      <div>
        <button type="button" onClick={() => void respondToApproval({ approved: true })}>允许执行</button>
        <button type="button" className="secondary" onClick={() => void respondToApproval({ approved: false, reason: "User declined" })}>拒绝</button>
      </div>
    </div> : null}
  </section>;
}

function UserMessage() {
  return <MessagePrimitive.Root className="mail-ai-message user">
    <ComposerPrimitive.If editing={false}>
      <MessagePrimitive.Parts components={{ Text: UserTextPart }} />
      <ActionBarPrimitive.Root className="mail-ai-action-bar" hideWhenRunning>
        <ActionBarPrimitive.Edit aria-label="编辑已发问题">编辑</ActionBarPrimitive.Edit>
      </ActionBarPrimitive.Root>
    </ComposerPrimitive.If>
    <ComposerPrimitive.If editing>
      <ComposerPrimitive.Root className="mail-ai-edit-composer">
        <ComposerPrimitive.Input aria-label="编辑已发问题" rows={3} />
        <div className="mail-ai-composer-actions">
          <ComposerPrimitive.Cancel>取消</ComposerPrimitive.Cancel>
          <ComposerPrimitive.Send>重新发送</ComposerPrimitive.Send>
        </div>
      </ComposerPrimitive.Root>
    </ComposerPrimitive.If>
  </MessagePrimitive.Root>;
}

function AssistantMessage() {
  return <MessagePrimitive.Root className="mail-ai-message assistant">
    <MessagePrimitive.Parts components={{ Text: AssistantMarkdownPart, Reasoning: () => <div className="mail-ai-thinking">正在分析…</div>, tools: { Fallback: ToolCard } }} />
    <ActionBarPrimitive.Root className="mail-ai-action-bar" hideWhenRunning>
      <ActionBarPrimitive.Copy aria-label="复制回答">复制</ActionBarPrimitive.Copy>
      <ActionBarPrimitive.Reload aria-label="重新执行">重试</ActionBarPrimitive.Reload>
    </ActionBarPrimitive.Root>
  </MessagePrimitive.Root>;
}

function AssistantThread({ empty }: { empty: boolean }) {
  return <ThreadPrimitive.Root className="mail-ai-thread">
    <ThreadPrimitive.Viewport className="mail-ai-assistant-viewport">
      {empty ? <div className="mail-ai-empty">可以搜索三个邮箱、阅读线程、总结邮件或起草回复。工具执行过程会显示在这里。</div> : null}
      <ThreadPrimitive.Messages components={{ UserMessage, AssistantMessage }} />
      <div className="mail-ai-suggestions" aria-label="Mail AI follow-up suggestions">
        <ThreadPrimitive.Suggestions>
          {({ suggestion }) => <SuggestionPrimitive.Trigger send>{suggestion.prompt}</SuggestionPrimitive.Trigger>}
        </ThreadPrimitive.Suggestions>
      </div>
      <ThreadPrimitive.ViewportFooter className="mail-ai-assistant-footer">
        <ComposerPrimitive.Root className="mail-ai-composer">
          <ComposerPrimitive.Input placeholder="问这封邮件，或搜索全部邮箱…" rows={3} />
          <div className="mail-ai-composer-actions"><ComposerPrimitive.Cancel>停止</ComposerPrimitive.Cancel><ComposerPrimitive.Send>发送</ComposerPrimitive.Send></div>
        </ComposerPrimitive.Root>
      </ThreadPrimitive.ViewportFooter>
    </ThreadPrimitive.Viewport>
  </ThreadPrimitive.Root>;
}

function MailSideChatRuntime({ csrf, thread, onPersisted }: { csrf: string; thread: ThreadSnapshot; onPersisted: () => void }) {
  const suggestionAdapter = useMemo(() => createSuggestionAdapter({
    key: `mail-a7:${thread.scope}`,
    count: 3,
    maxMessages: 6,
    complete: async ({ prompt, signal }) => signal?.aborted ? [] : mailAIFollowUpPrompts(thread.scope, prompt)
  }), [thread.scope]);
  const transport = useMemo(() => new AssistantChatTransport({
    api: "/api/mail-ai/agent",
    headers: { "X-CSRF-Token": csrf },
    body: { thread_id: thread.thread_id }
  }), [csrf, thread.thread_id]);
  const runtime = useChatRuntime<UIMessage>({
    messages: thread.messages,
    transport,
    adapters: { suggestion: suggestionAdapter },
    sendAutomaticallyWhen: lastAssistantMessageIsCompleteWithApprovalResponses,
    onFinish: ({ messages, isAbort, isDisconnect, isError }) => {
      if (isAbort || isDisconnect || isError) return;
      void fetch("/api/mail-ai/thread", {
        method: "PUT",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
        body: JSON.stringify({ thread_id: thread.thread_id, messages })
      }).then((response) => {
        if (!response.ok) throw new Error(`Mail AI persistence failed (${response.status})`);
        onPersisted();
      }).catch(() => console.error("Mail AI request failed without logging private content"));
    }
  });
  return <AssistantRuntimeProvider runtime={runtime}>
    <AssistantThread empty={thread.messages.length === 0} />
  </AssistantRuntimeProvider>;
}

function threadTitle(thread: ThreadSummary | ThreadSnapshot) {
  return thread.preview || thread.context?.subject || "新会话";
}

function threadTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString([], { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

export function MailSideChat({ csrf, messageID, defaultOpen = false }: { csrf: string; messageID: number; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(defaultOpen);
  const [model, setModel] = useState("Agent");
  const [thread, setThread] = useState<ThreadSnapshot | null>(null);
  const [history, setHistory] = useState<ThreadSummary[]>([]);
  const [trace, setTrace] = useState<TraceEvent[]>([]);
  const [threadError, setThreadError] = useState("");
  const [showHistory, setShowHistory] = useState(false);
  const [showTrace, setShowTrace] = useState(false);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);

  const loadHistory = useCallback(() => {
    void fetch("/api/mail-ai/threads", { headers: { Accept: "application/json" }, cache: "no-store" })
      .then(async (response) => {
        if (!response.ok) throw new Error(`Mail AI history failed (${response.status})`);
        return await response.json() as ThreadListResponse;
      })
      .then((value) => setHistory(Array.isArray(value.threads) ? value.threads : []))
      .catch(() => console.error("Mail AI request failed without logging private content"));
  }, []);

  const loadTrace = useCallback((threadID: string) => {
    void fetch(`/api/mail-ai/trace?thread_id=${encodeURIComponent(threadID)}`, { headers: { Accept: "application/json" }, cache: "no-store" })
      .then(async (response) => {
        if (!response.ok) throw new Error(`Mail AI trace failed (${response.status})`);
        return await response.json() as TraceResponse;
      })
      .then((value) => setTrace(Array.isArray(value.events) ? value.events : []))
      .catch(() => console.error("Mail AI request failed without logging private content"));
  }, []);

  const loadThread = useCallback(async (url: string) => {
    setBusy(true);
    try {
      const response = await fetch(url, { headers: { Accept: "application/json" }, cache: "no-store" });
      if (!response.ok) throw new Error(`Mail AI history failed (${response.status})`);
      const value = await response.json() as ThreadSnapshot;
      if (!value.thread_id || !Array.isArray(value.messages)) throw new Error("Mail AI history response is invalid");
      setThread(value);
      setThreadError("");
      return value;
    } finally {
      setBusy(false);
    }
  }, []);

  useEffect(() => {
    let active = true;
    void fetch("/api/mail-ai/model-config", { headers: { Accept: "application/json" }, cache: "no-store" })
      .then(async (response) => response.ok ? await response.json() as ModelConfig : null)
      .then((value) => { if (active && value?.model) setModel(value.model); })
      .catch(() => undefined);
    const params = new URLSearchParams({ scope: mailAIWorkspaceInitialScope(messageID) });
    if (messageID > 0) params.set("message_id", String(messageID));
    void loadThread(`/api/mail-ai/thread?${params.toString()}`)
      .then(() => { if (active) loadHistory(); })
      .catch((error) => { if (active) setThreadError(error instanceof Error ? error.message : "Mail AI history unavailable"); });
    return () => { active = false; };
  }, [loadHistory, loadThread, messageID]);

  const restoreThread = async (threadID: string) => {
    try {
      const restored = await loadThread(`/api/mail-ai/thread?thread_id=${encodeURIComponent(threadID)}`);
      if (showTrace) loadTrace(restored.thread_id);
      setShowHistory(false);
      setShowNew(false);
    } catch (error) {
      setThreadError(error instanceof Error ? error.message : "Mail AI history unavailable");
    }
  };

  const createThread = async (scope: ThreadScope) => {
    setBusy(true);
    try {
      const response = await fetch("/api/mail-ai/threads", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
        body: JSON.stringify({ scope, message_id: messageID })
      });
      if (!response.ok) throw new Error(`Mail AI new conversation failed (${response.status})`);
      const value = await response.json() as ThreadSnapshot;
      setThread(value);
      setTrace([]);
      setThreadError("");
      setShowNew(false);
      setShowHistory(false);
      setShowTrace(false);
      loadHistory();
    } catch (error) {
      setThreadError(error instanceof Error ? error.message : "Mail AI new conversation unavailable");
    } finally {
      setBusy(false);
    }
  };

  if (!open) {
    return <button className="mail-ai-launch" type="button" onClick={() => setOpen(true)}>AI</button>;
  }
  const availableScopes = mailAIWorkspaceAvailableScopes(messageID);
  return <aside className="mail-ai-panel mail-ai-panel-assistant" aria-label="Mail AI Side Chat">
    <button className="mail-ai-close ghost" type="button" onClick={() => setOpen(false)} aria-label="Close Mail AI">×</button>
    <header className="mail-ai-assistant-header">
      <div className="mail-ai-header-title">
        <strong>Mail AI</strong>
        <span>{thread ? `${mailAIScopeLabel(thread.scope)} · ${thread.context?.subject || model}` : model}</span>
      </div>
      <div className="mail-ai-header-actions">
        <button type="button" className="ghost" disabled={busy} onClick={() => { setShowNew((value) => !value); setShowHistory(false); setShowTrace(false); }}>新建</button>
        <button type="button" className="ghost" disabled={busy} onClick={() => { setShowHistory((value) => !value); setShowNew(false); setShowTrace(false); loadHistory(); }}>历史</button>
        <button type="button" className="ghost" disabled={busy || !thread} onClick={() => {
          const next = !showTrace;
          setShowTrace(next);
          setShowHistory(false);
          setShowNew(false);
          if (next && thread) loadTrace(thread.thread_id);
        }}>过程</button>
      </div>
    </header>
    {showNew ? <section className="mail-ai-new-menu" aria-label="New Mail AI conversation">
      <strong>新建会话范围</strong>
      <div>{availableScopes.map((scope) => <button type="button" key={scope} disabled={busy} onClick={() => void createThread(scope)}>{mailAIScopeLabel(scope)}</button>)}</div>
    </section> : null}
    {showHistory ? <section className="mail-ai-history" aria-label="Mail AI conversation history">
      {history.length === 0 ? <div className="mail-ai-empty">还没有历史会话。</div> : history.map((item) => <button
        type="button"
        key={item.thread_id}
        className={`mail-ai-history-item${item.thread_id === thread?.thread_id ? " active" : ""}`}
        onClick={() => void restoreThread(item.thread_id)}
      >
        <span className="mail-ai-history-row"><strong>{mailAIScopeLabel(item.scope)}</strong><time>{threadTime(item.updated_at)}</time></span>
        <span className="mail-ai-history-title">{threadTitle(item)}</span>
      </button>)}
    </section> : null}
    {showTrace ? <section className="mail-ai-trace mail-ai-timeline" aria-label="Mail AI work timeline">
      <div className="mail-ai-trace-note"><strong>工作时间线</strong><span>浏览 → 搜索 → 读取 → 比较 → 动作 → 结论；仅显示可审计证据，不包含模型私有推理。</span></div>
      {(() => {
        const timeline = mailAIWorkflowTimeline(trace, thread?.messages || []);
        return timeline.length === 0 ? <div className="mail-ai-empty">这个会话还没有可验证工作步骤。</div> : timeline.map((item, index) => <article className={`mail-ai-trace-item mail-ai-timeline-item state-${item.state}`} key={`${item.id}-${index}`}>
          <div className="mail-ai-timeline-rail"><span className="mail-ai-trace-index">{index + 1}</span><i aria-hidden="true" /></div>
          <div>
            <span className="mail-ai-timeline-phase">{item.phase}</span>
            <strong>{item.title}</strong>
            <span>{item.detail}</span>
          </div>
        </article>);
      })()}
    </section> : null}
    {threadError ? <div className="mail-ai-empty mail-ai-error">{threadError}</div> : null}
    {!thread && !threadError ? <div className="mail-ai-empty">正在恢复 Mail AI 对话…</div> : null}
    {thread ? <div className="mail-ai-runtime-shell" key={thread.thread_id}>
      <MailSideChatRuntime csrf={csrf} thread={thread} onPersisted={() => { loadHistory(); if (showTrace) loadTrace(thread.thread_id); }} />
    </div> : null}
  </aside>;
}
