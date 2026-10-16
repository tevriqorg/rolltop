import http from 'node:http';
import { Readable } from 'node:stream';
import { readFile, mkdir, writeFile, rename, chmod } from 'node:fs/promises';
import { dirname } from 'node:path';
import { createOpenAICompatible } from '@ai-sdk/openai-compatible';
import { ToolLoopAgent, createAgentUIStreamResponse, isStepCount, tool } from 'ai';
import { z } from 'zod';
import { assertMailAIWorkspaceTools, mailAIWorkspaceInstructions } from './workspace.mjs';
import { mailAIToolApprovalStatus } from './approval-policy.mjs';
import { nextModelConfig, normalizeModelConfigValues } from './model-config.mjs';

const addr = process.env.MAIL_AI_ADDR || '127.0.0.1:18091';
const [host, portText] = addr.split(':');
const port = Number(portText || '18091');
const hubURL = (process.env.MAIL_AI_HUB_URL || 'http://127.0.0.1:18090').replace(/\/$/, '');
const shared = process.env.MAIL_AI_SHARED_TOKEN || '';
const modelConfigFile = String(process.env.MAIL_AI_MODEL_CONFIG_FILE || '').trim();
let modelConfig = {
  baseURL: (process.env.MAIL_AI_MODEL_BASE_URL || '').replace(/\/$/, ''),
  model: process.env.MAIL_AI_MODEL || '',
  apiKey: process.env.MAIL_AI_MODEL_API_KEY || '',
};
let modelHealth = modelConfig.baseURL && modelConfig.model ? 'unknown' : 'unconfigured';

function authorized(req) {
  return shared && req.headers.authorization === `Bearer ${shared}`;
}
async function readJSON(req, limit = 512 * 1024) {
  const chunks = []; let size = 0;
  for await (const chunk of req) {
    size += chunk.length;
    if (size > limit) throw new Error('request too large');
    chunks.push(chunk);
  }
  return JSON.parse(Buffer.concat(chunks).toString('utf8') || '{}');
}

function latestUserText(messages = []) {
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i];
    if (m?.role !== 'user') continue;
    if (typeof m.content === 'string') return m.content.trim();
    if (Array.isArray(m.parts)) {
      return m.parts.filter(p => p?.type === 'text').map(p => p.text || '').join('\n').trim();
    }
  }
  return '';
}

async function hubTool(userID, name, args, confirmed = false) {
  const response = await fetch(`${hubURL}/api/mail-ai/tools`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', authorization: `Bearer ${shared}` },
    body: JSON.stringify({ user_id: userID, name, arguments: args, ...(confirmed ? { confirmed: true } : {}) }),
  });
  const text = await response.text();
  let payload;
  try { payload = JSON.parse(text); } catch { payload = { error: text || `HTTP ${response.status}` }; }
  if (!response.ok) return { ok: false, status: response.status, error: payload?.error || text };
  return payload?.result ?? payload;
}

function createTools({ userID, context, fallbackQuery }) {
  return assertMailAIWorkspaceTools({
    list_mail: tool({
      description: 'List recent mail metadata across connected mailboxes without loading message bodies. Use this first for broad mailbox overview questions, deadlines, conflicts, action items, or when search terms are unknown.',
      inputSchema: z.object({ account: z.string().trim().optional(), limit: z.number().int().min(1).max(20).optional() }),
      execute: async ({ account, limit }) => hubTool(userID, 'list_mail', { ...(account ? { account } : {}), limit: limit || 20 }),
    }),
    search_mail: tool({
      description: 'Search all connected mailboxes in Rolltop. Use this for cross-account lookup.',
      inputSchema: z.object({ q: z.string().trim().optional() }),
      execute: async ({ q }) => hubTool(userID, 'search_mail', { q: q || fallbackQuery || context.subject || 'mail' }),
    }),
    get_message: tool({
      description: 'Read one message from the local Rolltop mirror.',
      inputSchema: z.object({ message_id: z.number().int().positive().optional() }),
      execute: async ({ message_id }) => hubTool(userID, 'get_message', { message_id: message_id || context.message_id }),
    }),
    get_thread: tool({
      description: 'Read the current or named provider thread from Rolltop.',
      inputSchema: z.object({ account: z.string().trim().optional(), thread_id: z.string().trim().optional() }),
      execute: async ({ account, thread_id }) => hubTool(userID, 'get_thread', {
        account: account || context.account,
        thread_id: thread_id || context.thread_id,
      }),
    }),
    list_mailboxes: tool({
      description: 'List Rolltop mailbox destinations and roles for the current user. Use before move when the destination is not already explicit.',
      inputSchema: z.object({}),
      execute: async () => hubTool(userID, 'list_mailboxes', {}),
    }),
    draft_reply: tool({
      description: 'Create reply draft text only. This never sends email.',
      inputSchema: z.object({ message_id: z.number().int().positive().optional(), body: z.string().trim().min(1) }),
      execute: async ({ message_id, body }) => hubTool(userID, 'draft_reply', { message_id: message_id || context.message_id, body }),
    }),
    add_label: tool({
      description: 'Add a label to a message. This is a low-risk mail-state mutation governed by the Mail AI approval policy.',
      inputSchema: z.object({ message_id: z.number().int().positive().optional(), label: z.string().trim().min(1) }),
      execute: async ({ message_id, label }) => hubTool(userID, 'add_label', { message_id: message_id || context.message_id, label }, true),
    }),
    archive_message: tool({
      description: 'Archive a message. This is a low-risk mail-state mutation governed by the Mail AI approval policy.',
      inputSchema: z.object({ message_id: z.number().int().positive().optional() }),
      execute: async ({ message_id }) => hubTool(userID, 'archive_message', { message_id: message_id || context.message_id }, true),
    }),
    mark_read: tool({
      description: 'Set message read state. This is a low-risk mail-state mutation governed by the Mail AI approval policy.',
      inputSchema: z.object({ message_id: z.number().int().positive().optional(), read: z.boolean() }),
      execute: async ({ message_id, read }) => hubTool(userID, 'mark_read', { message_id: message_id || context.message_id, read }, true),
    }),
    star: tool({
      description: 'Star a message through Rolltop mail state. This is a low-risk mutation governed by the Mail AI approval policy.',
      inputSchema: z.object({ message_id: z.number().int().positive().optional() }),
      execute: async ({ message_id }) => hubTool(userID, 'star', { message_id: message_id || context.message_id }, true),
    }),
    unstar: tool({
      description: 'Remove the star from a message through Rolltop mail state. This is a low-risk mutation governed by the Mail AI approval policy.',
      inputSchema: z.object({ message_id: z.number().int().positive().optional() }),
      execute: async ({ message_id }) => hubTool(userID, 'unstar', { message_id: message_id || context.message_id }, true),
    }),
    move: tool({
      description: 'Move a message to another mailbox in the same account using Rolltop mail operations. Use list_mailboxes when needed. This is a low-risk mutation governed by the Mail AI approval policy.',
      inputSchema: z.object({ message_id: z.number().int().positive().optional(), mailbox: z.string().trim().optional(), mailbox_id: z.number().int().positive().optional() }),
      execute: async ({ message_id, mailbox, mailbox_id }) => hubTool(userID, 'move', { message_id: message_id || context.message_id, ...(mailbox ? { mailbox } : {}), ...(mailbox_id ? { mailbox_id } : {}) }, true),
    }),
    snooze: tool({
      description: 'Snooze a message until an RFC3339 timestamp. This is a low-risk mutation governed by the Mail AI approval policy.',
      inputSchema: z.object({ message_id: z.number().int().positive().optional(), until: z.string().trim().min(1) }),
      execute: async ({ message_id, until }) => hubTool(userID, 'snooze', { message_id: message_id || context.message_id, until }, true),
    }),
    trash: tool({
      description: 'Move a message to Trash using the account/provider capability. This is a high-impact action and always requires explicit user approval immediately before execution.',
      inputSchema: z.object({ message_id: z.number().int().positive().optional() }),
      execute: async ({ message_id }) => hubTool(userID, 'trash', { message_id: message_id || context.message_id }, true),
    }),
    restore: tool({
      description: 'Restore a trashed message to Inbox using the account/provider capability. This is a low-risk mutation governed by the Mail AI approval policy.',
      inputSchema: z.object({ message_id: z.number().int().positive().optional() }),
      execute: async ({ message_id }) => hubTool(userID, 'restore', { message_id: message_id || context.message_id }, true),
    }),
    create_draft: tool({
      description: 'Create and persist a real Rolltop draft. This never sends the message.',
      inputSchema: z.object({ to: z.string().trim().optional(), cc: z.string().trim().optional(), bcc: z.string().trim().optional(), subject: z.string().trim().optional(), body: z.string().trim().min(1), from_identity_id: z.number().int().positive().optional() }),
      execute: async (args) => hubTool(userID, 'create_draft', args),
    }),
    reply_mail: tool({
      description: 'Send a reply using Rolltop server-owned reply recipients, threading and compose pipeline. Requires explicit user approval before execution.',
      inputSchema: z.object({ message_id: z.number().int().positive().optional(), body: z.string().trim().min(1), reply_all: z.boolean().optional() }),
      execute: async ({ message_id, body, reply_all }) => hubTool(userID, 'reply_mail', { message_id: message_id || context.message_id, body, reply_all: Boolean(reply_all) }, true),
    }),
    forward_mail: tool({
      description: 'Forward a message through Rolltop compose/outbox. Requires explicit user approval before execution.',
      inputSchema: z.object({ message_id: z.number().int().positive().optional(), to: z.string().trim().min(1), cc: z.string().trim().optional(), bcc: z.string().trim().optional(), body: z.string().trim().optional(), include_attachments: z.boolean().optional() }),
      execute: async ({ message_id, ...args }) => hubTool(userID, 'forward_mail', { message_id: message_id || context.message_id, ...args }, true),
    }),
    send_mail: tool({
      description: 'Send a new email through Rolltop compose/outbox. Requires explicit user approval before execution; never call without confirmed recipients and content.',
      inputSchema: z.object({ to: z.string().trim().min(1), cc: z.string().trim().optional(), bcc: z.string().trim().optional(), subject: z.string().trim().optional(), body: z.string().trim().min(1), from_identity_id: z.number().int().positive().optional() }),
      execute: async (args) => hubTool(userID, 'send_mail', args, true),
    }),
    present_mail_ui: tool({
      description: 'Present a safe Mail AI UI component. Only the fixed vocabulary is allowed; never emit HTML or executable UI.',
      inputSchema: z.object({
        kind: z.enum(['deadline', 'risk', 'reply', 'conflict', 'table', 'steps', 'form']),
        title: z.string().trim().min(1).max(160), summary: z.string().trim().max(1200).optional(),
        message_ids: z.array(z.number().int().positive()).max(8).optional(),
        items: z.array(z.object({ label: z.string().max(160), value: z.string().max(500), message_id: z.number().int().positive().optional() })).max(12).optional(),
        columns: z.array(z.string().max(80)).max(8).optional(), rows: z.array(z.array(z.string().max(240)).max(8)).max(12).optional(),
        steps: z.array(z.string().max(400)).max(10).optional(),
        fields: z.array(z.object({ name: z.string().max(80), label: z.string().max(120), type: z.enum(['text', 'date', 'select']), value: z.string().max(400).optional() })).max(8).optional(),
      }),
      execute: async (ui) => ({ ok: true, ui }),
    }),
    list_workspace_items: tool({
      description: 'List the persistent Mail AI home workbench items: replies, deadlines, waiting-on-others, and risks.',
      inputSchema: z.object({}), execute: async () => hubTool(userID, 'list_workspace_items', {}),
    }),
    upsert_workspace_item: tool({
      description: 'Create or update one persistent Mail AI workbench item. This visible mutation follows the low-risk approval policy.',
      inputSchema: z.object({ id: z.string().trim().optional(), kind: z.enum(['reply', 'deadline', 'waiting', 'risk']), title: z.string().trim().min(1).max(240), detail: z.string().max(4000).optional(), message_id: z.number().int().positive().optional(), status: z.enum(['open', 'done']).optional(), due_at: z.string().max(80).optional() }),
      execute: async (args) => hubTool(userID, 'upsert_workspace_item', args, true),
    }),
    read_attachments: tool({
      description: 'Read attachment content only on demand. Omit attachment_ids to list bounded metadata first; at most 3 explicit attachments may be read.',
      inputSchema: z.object({ message_id: z.number().int().positive().optional(), attachment_ids: z.array(z.number().int().positive()).max(3).optional() }),
      execute: async ({ message_id, attachment_ids }) => hubTool(userID, 'read_attachments', { message_id: message_id || context.message_id, ...(attachment_ids ? { attachment_ids } : {}) }),
      toModelOutput: ({ output }) => {
        const result = output?.result || output;
        const attachments = Array.isArray(result?.attachments) ? result.attachments : [];
        const parts = [];
        for (const item of attachments) {
          if (item?.text) parts.push({ type: 'text', text: `Attachment ${item.filename || item.id}:\n${item.text}` });
          if (item?.data && String(item.content_type || '').startsWith('image/')) {
            parts.push({ type: 'file', data: { type: 'data', data: item.data }, mediaType: item.content_type, filename: item.filename || undefined });
          }
        }
        return parts.length ? { type: 'content', value: parts } : { type: 'json', value: result ?? null };
      },
    }),
  });
}

function createAgent(body) {
  const provider = createOpenAICompatible({
    baseURL: modelConfig.baseURL,
    name: 'vela-mail-ai',
    ...(modelConfig.apiKey ? { apiKey: modelConfig.apiKey } : {}),
  });
  const context = body.context || {};
  const fallbackQuery = latestUserText(body.messages || []);
  const tools = createTools({ userID: body.user_id, context, fallbackQuery });
  return new ToolLoopAgent({
    model: provider.chatModel(modelConfig.model),
    stopWhen: isStepCount(6),
    instructions: mailAIWorkspaceInstructions(context),
    tools,
    toolApproval: ({ toolCall }) => mailAIToolApprovalStatus(toolCall.toolName),
  });
}
async function sendWebResponse(webResponse, res) {
  res.statusCode = webResponse.status;
  for (const [key, value] of webResponse.headers) res.setHeader(key, value);
  if (!webResponse.body) return res.end();
  Readable.fromWeb(webResponse.body).pipe(res);
}

async function probeConfig(candidate, timeoutMs = 4000) {
  const headers = candidate.apiKey ? { authorization: `Bearer ${candidate.apiKey}` } : {};
  const response = await fetch(`${candidate.baseURL}/models`, { headers, redirect: 'manual', signal: AbortSignal.timeout(timeoutMs) });
  if (!response.ok) throw new Error(`model endpoint /models returned HTTP ${response.status}`);
  const payload = await response.json();
  const ids = Array.isArray(payload?.data) ? payload.data.map(x => String(x?.id || '')) : [];
  if (!ids.includes(candidate.model)) throw new Error(`model ${candidate.model} was not returned by /models`);
}

async function persistConfig(candidate) {
  if (!modelConfigFile) return;
  await mkdir(dirname(modelConfigFile), { recursive: true, mode: 0o700 });
  const temp = `${modelConfigFile}.${process.pid}.tmp`;
  await writeFile(temp, JSON.stringify({ base_url: candidate.baseURL, model: candidate.model, api_key: candidate.apiKey }) + '\n', { mode: 0o600 });
  await chmod(temp, 0o600);
  await rename(temp, modelConfigFile);
  await chmod(modelConfigFile, 0o600);
}

async function loadPersistedConfig() {
  if (!modelConfigFile) return;
  try {
    const stored = JSON.parse(await readFile(modelConfigFile, 'utf8'));
    const normalized = normalizeModelConfigValues(stored.base_url, stored.model);
    modelConfig = { ...normalized, apiKey: String(stored.api_key || '') };
    modelHealth = 'unknown';
  } catch (error) {
    if (error?.code !== 'ENOENT') throw error;
  }
}

async function validateConfig(next) {
  const candidate = nextModelConfig(modelConfig, next);
  await probeConfig(candidate);
  await persistConfig(candidate);
  modelConfig = candidate;
  modelHealth = 'healthy';
}

async function refreshModelHealth() {
  if (!modelConfig.baseURL || !modelConfig.model) return modelHealth = 'unconfigured';
  try { await probeConfig(modelConfig, 2500); return modelHealth = 'healthy'; }
  catch { return modelHealth = 'unavailable'; }
}

function publicConfig() {
  return { configured: Boolean(modelConfig.baseURL && modelConfig.model), provider: 'openai-compatible', base_url: modelConfig.baseURL, model: modelConfig.model, health: modelHealth, api_key_configured: Boolean(modelConfig.apiKey) };
}
const server = http.createServer(async (req, res) => {
  try {
    if (req.url === '/healthz' && req.method === 'GET') {
      res.setHeader('content-type', 'application/json');
      return res.end(JSON.stringify({ ok: true, configured: publicConfig().configured, model: modelConfig.model, health: modelHealth, runtime: 'ai-sdk-tool-loop-agent' }));
    }
    if (!authorized(req)) { res.statusCode = 401; return res.end('unauthorized'); }
    if (req.url === '/config' && req.method === 'GET') {
      await refreshModelHealth();
      res.setHeader('content-type', 'application/json'); return res.end(JSON.stringify(publicConfig()));
    }
    if (req.url === '/config' && req.method === 'PUT') {
      const body = await readJSON(req, 32 * 1024); await validateConfig(body);
      res.setHeader('content-type', 'application/json'); return res.end(JSON.stringify(publicConfig()));
    }
    if (req.url !== '/agent' || req.method !== 'POST') { res.statusCode = 404; return res.end('not found'); }
    const body = await readJSON(req);
    if (!Number.isInteger(body.user_id) || body.user_id <= 0 || !body.context?.mode || !Array.isArray(body.messages)) {
      res.statusCode = 400; return res.end('invalid agent request');
    }
    if (!publicConfig().configured) { res.statusCode = 503; return res.end('model is not configured'); }
    const agent = createAgent(body);
    const response = await createAgentUIStreamResponse({ agent, uiMessages: body.messages, keepAliveMs: 15000 });
    return await sendWebResponse(response, res);
  } catch (error) {
    const errorType = error?.constructor?.name || typeof error;
    const status = Number.isInteger(error?.status) ? ` status=${error.status}` : '';
    const code = typeof error?.code === 'string' && /^[A-Za-z0-9_.-]{1,64}$/.test(error.code) ? ` code=${error.code}` : '';
    console.error(`mail-ai-agent request failed type=${errorType}${status}${code}`);
    if (!res.headersSent) res.statusCode = 500;
    if (!res.writableEnded) res.end('mail-ai agent request failed');
  }
});

await loadPersistedConfig();
server.listen(port, host, () => console.log(`mail-ai-agent listening on ${host}:${port}`));
