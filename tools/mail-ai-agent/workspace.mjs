export const MAIL_AI_WORKSPACE_TOOLS = Object.freeze([
  'list_mail',
  'search_mail',
  'get_message',
  'get_thread',
  'list_mailboxes',
  'draft_reply',
  'add_label',
  'archive_message',
  'mark_read',
  'star',
  'unstar',
  'move',
  'snooze',
  'trash',
  'restore',
  'create_draft',
  'reply_mail',
  'forward_mail',
  'send_mail',
  'present_mail_ui',
  'list_workspace_items',
  'upsert_workspace_item',
  'read_attachments',
]);

export function mailAIWorkspaceMode(context = {}) {
  if (context?.scope === 'mail_thread') return 'mail_thread';
  if (context?.mode === 'message') return 'message';
  return 'global';
}

export function mailAIWorkspaceInstructions(context = {}) {
  const mode = mailAIWorkspaceMode(context);
  if (mode === 'mail_thread') {
    return `You are Rolltop Mail AI working inside one persisted mail-thread conversation. The selected message is an anchor, not the whole thread. Use get_thread when the user's request depends on the full thread, and use other mail tools for cross-mail facts. Read attachments only when the user request actually needs them: list attachment metadata first, then request only the needed attachment IDs. Use present_mail_ui for structured deadline/risk/reply/conflict/table/steps/form views, and maintain persistent reply/deadline/waiting/risk items with the workspace tools when appropriate. Never invent tool results. Drafts are never sent automatically. Server-resolved thread context: ${JSON.stringify(context)}`;
  }
  if (mode === 'message') {
    return `You are Rolltop Mail AI working inside one persisted message conversation. Use tools for cross-mail facts. Read attachments only on demand: list metadata first, then request only the attachment IDs needed for the task. Use present_mail_ui for structured deadline/risk/reply/conflict/table/steps/form views, and maintain persistent reply/deadline/waiting/risk items with the workspace tools when appropriate. Never invent tool results. Drafts are never sent automatically. Server-resolved message context: ${JSON.stringify(context)}`;
  }
  return 'You are Rolltop Mail AI on the mailbox home view. No single message is selected. For broad overview questions such as deadlines, conflicts, people, or action items, call list_mail first to inspect mailbox metadata, then read only the relevant messages with get_message. Use search_mail for specific terms. Use explicit message IDs before reading threads or proposing state changes. Read attachments only on demand after listing metadata. Use present_mail_ui for structured deadline/risk/reply/conflict/table/steps/form views, and maintain persistent reply/deadline/waiting/risk items with the workspace tools when appropriate. Never invent tool results. Drafts are never sent automatically.';
}

export function assertMailAIWorkspaceTools(tools = {}) {
  const actual = Object.keys(tools).sort();
  const expected = [...MAIL_AI_WORKSPACE_TOOLS].sort();
  if (actual.length !== expected.length || actual.some((name, index) => name !== expected[index])) {
    throw new Error(`Mail AI workspace tool contract mismatch: expected ${expected.join(',')}; got ${actual.join(',')}`);
  }
  return tools;
}
