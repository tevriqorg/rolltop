// Stable semantic frontend action registry shared by Agent Action Mode and Guidance Mode.
// Handlers are React-owned capabilities; callers never inspect DOM selectors.
export type MailSemanticRisk = "read" | "review" | "high";
export type MailSemanticMode = "guide" | "action";

export type MailSemanticActionInput = unknown;
export type MailSemanticAction = {
  name: string;
  description: string;
  risk: MailSemanticRisk;
  run: (input?: MailSemanticActionInput) => void | Promise<void>;
  target?: () => HTMLElement | null;
};

type Listener = () => void;
const actions = new Map<string, MailSemanticAction>();
const listeners = new Set<Listener>();

function emit() {
  listeners.forEach((listener) => listener());
}

export function registerMailSemanticAction(action: MailSemanticAction): () => void {
  actions.set(action.name, action);
  emit();
  return () => {
    if (actions.get(action.name) === action) {
      actions.delete(action.name);
      emit();
    }
  };
}
export function listMailSemanticActions(): Array<Pick<MailSemanticAction, "name" | "description" | "risk">> {
  return Array.from(actions.values())
    .map(({ name, description, risk }) => ({ name, description, risk }))
    .sort((a, b) => a.name.localeCompare(b.name));
}

export function getMailSemanticAction(name: string): MailSemanticAction | undefined {
  return actions.get(name);
}

export async function performMailSemanticAction(
  name: string,
  input?: MailSemanticActionInput,
  options: { approved?: boolean } = {}
): Promise<void> {
  const action = actions.get(name);
  if (!action) throw new Error(`Semantic mail action is not available: ${name}`);
  if (action.risk === "high" && options.approved !== true) {
    throw new Error(`Semantic mail action requires explicit approval: ${name}`);
  }
  await action.run(input);
}

export function subscribeMailSemanticActions(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
export function semanticString(input: MailSemanticActionInput, key = "value"): string {
  if (typeof input === "string") return input;
  if (input && typeof input === "object" && key in input) {
    const value = (input as Record<string, unknown>)[key];
    return typeof value === "string" ? value : "";
  }
  return "";
}

export function semanticAccountSlug(value: string): string {
  const slug = value.trim().toLowerCase()
    .replace(/[^a-z0-9]+/g, ".")
    .replace(/^\.+|\.+$/g, "");
  return slug || "account";
}