export function normalizeModelConfigValues(baseURL, model) {
  const normalizedURL = String(baseURL || '').replace(/\/$/, '').trim();
  const normalizedModel = String(model || '').trim();
  if (!normalizedURL || !normalizedModel) throw new Error('base_url and model are required');
  const parsed = new URL(normalizedURL);
  if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password || parsed.search || parsed.hash) {
    throw new Error('base_url must be an absolute http(s) URL without credentials, query, or fragment');
  }
  return { baseURL: normalizedURL, model: normalizedModel };
}

export function nextModelConfig(current, next) {
  const normalized = normalizeModelConfigValues(next.base_url, next.model);
  const hasExplicitKey = Object.prototype.hasOwnProperty.call(next, 'api_key') && next.api_key !== undefined && next.api_key !== null;
  if (normalized.baseURL !== current.baseURL && current.apiKey && !next.clear_api_key && !hasExplicitKey) {
    throw new Error('changing base_url with a stored key requires a new api_key or clear_api_key');
  }
  const apiKey = next.clear_api_key ? '' : (hasExplicitKey ? String(next.api_key) : current.apiKey);
  return { ...normalized, apiKey };
}
