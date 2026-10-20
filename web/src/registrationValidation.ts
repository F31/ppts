// Match the server's account syntax and byte-based password policy.
export function registrationAccountError(raw: string): string | null {
  const value = raw.trim();
  if (value.includes('@')) {
    const parts = value.split('@');
    const domain = parts[1] ?? '';
    if (new TextEncoder().encode(value).length <= 254 && !value.includes(' ') &&
        !value.includes('..') && parts.length === 2 && parts[0] &&
        domain.includes('.') && !domain.startsWith('.') && !domain.endsWith('.')) return null;
  } else if (/^\+?[0-9 ()-]+$/.test(value)) {
    const digits = value.replace(/[ ()-]/g, '').replace(/^\+/, '');
    if (/^[0-9]{5,15}$/.test(digits)) return null;
  }
  return 'login.accountInvalid';
}

export function registrationPasswordError(password: string, account: string): string | null {
  const bytes = new TextEncoder().encode(password).length;
  if (bytes < 10 || bytes > 72) return 'login.passwordLength';
  const classes = [/[A-Z]/, /[a-z]/, /[0-9]/, /[^A-Za-z0-9]/].filter(re => re.test(password)).length;
  if (classes < 3) return 'login.passwordClasses';
  if (['password', 'password1', 'password123', '12345678', '123456789', '1234567890',
    'qwertyuiop', 'qwerty123', '11111111', '00000000', 'abc123456', 'iloveyou',
    'admin123', 'letmein123', 'welcome123', 'passw0rd', 'p@ssw0rd', '12345678910']
    .includes(password.toLowerCase())) return 'login.passwordCommon';
  const normalized = account.trim().toLowerCase();
  const local = normalized.includes('@') ? normalized.split('@')[0] : normalized.replace(/[ ()-]/g, '').replace(/^\+/, '');
  if (Array.from(local).length >= 4 && password.toLowerCase().includes(local)) return 'login.passwordAccount';
  return null;
}
