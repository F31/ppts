const tokenKey = 'pptsAccessToken';
const verifierKey = 'pptsPKCEVerifier';
const stateKey = 'pptsOIDCState';
const devIdentityKey = 'pptsDevIdentity';

export type DevIdentity = { tenantId: string; userId: string };

export function storedDevIdentity(): DevIdentity | null {
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

export function isDevIdentityEnabled() {
  // 开发身份进入需显式门控：仅本地开发或未配置 OIDC 时开放。
  return !oidcConfigured() || import.meta.env.DEV;
}

export function clearAllIdentity() {
  localStorage.removeItem(tokenKey);
  localStorage.removeItem(devIdentityKey);
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
