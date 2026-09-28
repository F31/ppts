// 消息服务配置（发件箱 / 短信网关）
//
// P2-C2：本文件是原 `src/api.ts` 的一部分，按域拆出，便于按功能定位而不是在近两千行里翻找。
// **只动了文件边界，实现代码逐行未改**；对外入口仍是 `../api`（由 index.ts 统一转出），
// 因此既有调用方的 import 路径无需任何改动。

import type { ClientIdentity } from './identity';
import { gatewayPath } from './gateway';
// ---- 消息服务配置（发件箱 / 短信网关，第二批）----
export type MessageEmailConfig = {
  enabled: boolean;
  host: string;
  port: number;
  username: string;
  from_address: string;
  from_name: string;
  tls_mode: string;
  has_password: boolean;
  password_masked: string;
  version: number;
  updated_at: number;
};

export type MessageSMSConfig = {
  enabled: boolean;
  provider: string;
  endpoint: string;
  sign_name: string;
  template_code: string;
  access_key_id: string;
  has_secret: boolean;
  secret_masked: string;
  version: number;
  updated_at: number;
};

export type MessageChannels = {
  is_operator: boolean;
  email?: MessageEmailConfig;
  platform_email?: MessageEmailConfig;
  sms?: MessageSMSConfig;
};

export type EmailChannelInput = {
  enabled: boolean;
  host: string;
  port: number;
  username: string;
  from_address: string;
  from_name: string;
  tls_mode: string;
  password?: string;
  platform_default?: boolean;
};

export type SMSChannelInput = {
  enabled: boolean;
  provider: string;
  endpoint: string;
  sign_name: string;
  template_code: string;
  access_key_id: string;
  access_key_secret?: string;
};

export async function getMessageChannels(identity: ClientIdentity): Promise<MessageChannels> {
  return (await gatewayPath(identity, 'GET', '/api/message-channels')) as MessageChannels;
}

export async function saveEmailChannel(identity: ClientIdentity, input: EmailChannelInput): Promise<MessageEmailConfig> {
  const data = (await gatewayPath(identity, 'PUT', '/api/message-channels/email', input)) as { email?: MessageEmailConfig };
  return data.email!;
}

export async function testEmailChannel(
  identity: ClientIdentity,
  to: string,
  platformDefault = false
): Promise<{ ok: boolean; error?: string }> {
  return (await gatewayPath(identity, 'POST', '/api/message-channels/email/test', {
    to,
    platform_default: platformDefault
  })) as { ok: boolean; error?: string };
}

export async function saveSmsChannel(identity: ClientIdentity, input: SMSChannelInput): Promise<MessageSMSConfig> {
  const data = (await gatewayPath(identity, 'PUT', '/api/message-channels/sms', input)) as { sms?: MessageSMSConfig };
  return data.sms!;
}
