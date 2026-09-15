import { ConnectError } from './api';

export type Translator = (key: string) => string;

// describeApiError 是 A26「无接口功能：隐藏或明确不可用」的前端统一出口。
//
// 背景：此前多处用 `.catch(() => null)` / `.catch(() => {})` 把接口错误吞掉，
// 渲染成"暂无数据 / 暂不可用"，用户与开发者都无法区分
// 「服务端没有这个能力」「没有权限」「网络/服务异常」「真的没有数据」四种情况，
// 既掩盖了真实故障，也违反"不留假状态"的要求。
//
// 约定：任何"可能失败且结果会渲染成空态"的请求，都必须走这里把原因显式带到界面上；
// 调用方负责同时提供重试入口（修复入口）。
export function describeApiError(error: unknown, fallback: string, t?: Translator): string {
  if (error instanceof ConnectError) {
    // ConnectError 的 code 对原生 HTTP 端点是 `http_<status>`，对 Connect RPC 是标准码。
    if (t) {
      if (error.code === 'unimplemented') return t('err.unimplemented');
      if (error.code === 'http_404' || error.code === 'not_found') return t('err.notFound');
      if (error.code === 'http_401' || error.code === 'unauthenticated') return t('err.unauthenticated');
      if (error.code === 'http_403' || error.code === 'permission_denied') return t('err.forbidden');
      if (error.code === 'http_503' || error.code === 'unavailable') return t('err.unavailable');
    }
    return `${fallback}（${error.code}）`;
  }
  if (error instanceof Error && error.message) return `${fallback}：${error.message}`;
  return fallback;
}

// isNotFound 判定"资源确实不存在"，用于区分 404（正常业务语义，如讲解尚未生成）
// 与其他错误（异常，必须显式报错而非降级成同一文案）。
export function isNotFound(error: unknown): boolean {
  return error instanceof ConnectError && (error.code === 'http_404' || error.code === 'not_found');
}

// Settled 是"取值结果"：要么拿到数据（error 为 null），要么拿到错误（data 为 null）。
export type Settled<T> = { data: T | null; error: unknown };

// settle 把"可能失败的取值"变成显式结果，替代 `.catch(() => null)`。
// 这样调用方既能渲染降级值，又能把真实原因交给 describeApiError 显示到界面上。
export async function settle<T>(fn: () => Promise<T>): Promise<Settled<T>> {
  try {
    return { data: await fn(), error: null };
  } catch (error) {
    return { data: null, error };
  }
}
