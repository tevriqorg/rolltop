export const MAIL_AI_AUTOMATIC_TOOLS = Object.freeze([
  'list_mail', 'search_mail', 'get_message', 'get_thread', 'list_mailboxes',
  'draft_reply', 'create_draft',
]);

export const MAIL_AI_LOW_RISK_MUTATIONS = Object.freeze([
  'add_label', 'archive_message', 'mark_read', 'star', 'unstar', 'move', 'snooze', 'restore',
]);

export const MAIL_AI_HIGH_RISK_ACTIONS = Object.freeze([
  'trash', 'reply_mail', 'forward_mail', 'send_mail',
]);

export function mailAIToolRisk(toolName) {
  if (MAIL_AI_AUTOMATIC_TOOLS.includes(toolName)) return 'automatic';
  if (MAIL_AI_LOW_RISK_MUTATIONS.includes(toolName)) return 'review';
  if (MAIL_AI_HIGH_RISK_ACTIONS.includes(toolName)) return 'high';
  return 'unknown';
}

export function mailAIToolApprovalStatus(toolName, lowRiskMode = process.env.MAIL_AI_LOW_RISK_APPROVAL || 'confirm') {
  const risk = mailAIToolRisk(toolName);
  if (risk === 'high') return 'user-approval';
  if (risk === 'review') return lowRiskMode === 'automatic' ? undefined : 'user-approval';
  return undefined;
}
