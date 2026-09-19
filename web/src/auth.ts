import type { ClientIdentity } from './api';

const tokenKey = 'pptsAccessToken';
const verifierKey = 'pptsPKCEVerifier';
const stateKey = 'pptsOIDCState';
const devIdentityKey = 'pptsDevIdentity';

export type DevIdentity = { tenantId: string; userId: string };

export function storedDevIdentity(): DevIdentity | null {
  // A02：开发身份不得混入生产。未开放该能力的构建里，即使浏览器残留 pptsDevIdentity
  // 也不采纳（否则会在无 token 的情况下继续发 X-PPTS-Tenant-ID / X-PPTS-User-ID）。
  if (!isDevIdentityEnabled()) return null;
  const raw = localStorage.getItem(devIdentityKey);
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as DevIdentity;
    if (parsed.tenantId && parsed.userId) return parsed;
    return null;
  } catch {
    return null;
  }
}

export function saveDevIdentity(identity: DevIdentity) {
  localStorage.setItem(devIdentityKey, JSON.stringify(identity));
}

const localIdentityKey = 'pptsLocalIdentity';

// 单租户本地模式身份（SQLite profile）：后端无登录，前端以固定本地身份自动进入。
// 与"开发身份"不同——这是后端经 /auth/config 明确告知的正式运行模式（local=true），
// 因此不受 VITE_ALLOW_DEV_IDENTITY 门控，刷新后直接恢复。
export function storedLocalIdentity(): DevIdentity | null {
  const raw = localStorage.getItem(localIdentityKey);
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as DevIdentity;
    if (parsed.tenantId && parsed.userId) return parsed;
    return null;
  } catch {
    return null;
  }
}

export function saveLocalIdentity(identity: DevIdentity) {
  localStorage.setItem(localIdentityKey, JSON.stringify(identity));
}

// 开发身份（无 token 的 X-PPTS-Tenant-ID / X-PPTS-User-ID）是**显式门控**能力（A01/A02）。
// 生产构建默认关闭：登录页不渲染开发身份表单，已存的 dev identity 也不被采纳，
// 未配置 OIDC 时只提示「登录服务尚未配置」，不回退。
// 本地开发（import.meta.env.DEV）默认开放；生产构建确需（如离线端到端验收）时，
// 必须在构建时显式声明 VITE_ALLOW_DEV_IDENTITY=true —— 让开放成为一次可审计的显式决定，
// 而不是"未配置 OIDC"的隐式回退。
export function isDevIdentityEnabled() {
  return import.meta.env.DEV || import.meta.env.VITE_ALLOW_DEV_IDENTITY === 'true';
}

const identityKey = 'pptsIdentity';

// 邮箱登录身份（含真实 tenantId/userId/accessToken）持久化，刷新后恢复真实身份。
export function saveIdentity(identity: ClientIdentity) {
  localStorage.setItem(identityKey, JSON.stringify(identity));
}

export function storedIdentity(): ClientIdentity | null {
  const raw = localStorage.getItem(identityKey);
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as ClientIdentity;
    if (parsed.tenantId && parsed.userId && parsed.accessToken) return parsed;
    return null;
  } catch {
    return null;
  }
}

export function clearAllIdentity() {
  localStorage.removeItem(tokenKey);
  localStorage.removeItem(devIdentityKey);
  localStorage.removeItem(identityKey);
  localStorage.removeItem(localIdentityKey);
}

function base64URL(bytes: ArrayBuffer | Uint8Array) {
  const data = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
  return btoa(String.fromCharCode(...data)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function randomString(bytes = 32) {
  const data = new Uint8Array(bytes);
  crypto.getRandomValues(data);
  return base64URL(data);
}

async function codeChallenge(verifier: string) {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier));
  return base64URL(digest);
}

export function storedAccessToken() {
  return localStorage.getItem(tokenKey) ?? '';
}

export function clearAccessToken() {
  localStorage.removeItem(tokenKey);
}

export function oidcConfigured() {
  return Boolean(
    import.meta.env.VITE_OIDC_AUTHORIZATION_ENDPOINT &&
      import.meta.env.VITE_OIDC_TOKEN_ENDPOINT &&
      import.meta.env.VITE_OIDC_CLIENT_ID
  );
}

export async function startOIDCLogin() {
  if (!oidcConfigured()) {
    throw new Error('OIDC is not configured');
  }
  const verifier = randomString(48);
  const state = randomString(24);
  localStorage.setItem(verifierKey, verifier);
  localStorage.setItem(stateKey, state);
  const redirectURI = import.meta.env.VITE_OIDC_REDIRECT_URI || window.location.origin + window.location.pathname;
  const url = new URL(import.meta.env.VITE_OIDC_AUTHORIZATION_ENDPOINT);
  url.searchParams.set('response_type', 'code');
  url.searchParams.set('client_id', import.meta.env.VITE_OIDC_CLIENT_ID);
  url.searchParams.set('redirect_uri', redirectURI);
  url.searchParams.set('scope', import.meta.env.VITE_OIDC_SCOPE || 'openid profile email');
  url.searchParams.set('state', state);
  url.searchParams.set('code_challenge_method', 'S256');
  url.searchParams.set('code_challenge', await codeChallenge(verifier));
  window.location.assign(url.toString());
}

export async function completeOIDCCallback() {
  const url = new URL(window.location.href);
  const code = url.searchParams.get('code');
  const state = url.searchParams.get('state');
  if (!code) return '';
  const expectedState = localStorage.getItem(stateKey);
  const verifier = localStorage.getItem(verifierKey);
  if (!state || state !== expectedState || !verifier) {
    throw new Error('OIDC state mismatch');
  }
  const redirectURI = import.meta.env.VITE_OIDC_REDIRECT_URI || window.location.origin + window.location.pathname;
  const body = new URLSearchParams({
    grant_type: 'authorization_code',
    client_id: import.meta.env.VITE_OIDC_CLIENT_ID,
    code,
    redirect_uri: redirectURI,
    code_verifier: verifier
  });
  const response = await fetch(import.meta.env.VITE_OIDC_TOKEN_ENDPOINT, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body
  });
  if (!response.ok) {
    throw new Error(`OIDC token exchange failed: HTTP ${response.status}`);
  }
  const token = (await response.json()) as { access_token?: string; id_token?: string };
  const bearer = token.access_token || token.id_token || '';
  if (!bearer) {
    throw new Error('OIDC token response missing access_token/id_token');
  }
  localStorage.setItem(tokenKey, bearer);
  localStorage.removeItem(verifierKey);
  localStorage.removeItem(stateKey);
  url.searchParams.delete('code');
  url.searchParams.delete('state');
  window.history.replaceState({}, document.title, url.toString());
  return bearer;
}
