import { useCallback, useEffect, useState } from 'react';
import { createDictionary, deleteDictionary, listDictionaries, updateDictionary, type ClientIdentity } from '../api';
import type { PronunciationDictionary, PronunciationRule } from '../types';

type EditorState = {
  id: string; // 空=新建
  name: string;
  rules: PronunciationRule[];
};

const emptyRule: PronunciationRule = { pattern: '', replacement: '', enabled: true };

export function SettingsDictionary({ identity }: { identity: ClientIdentity }) {
  const [dictionaries, setDictionaries] = useState<PronunciationDictionary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [editor, setEditor] = useState<EditorState | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      setDictionaries(await listDictionaries(identity));
    } catch (err) {
      setError(err instanceof Error ? err.message : '词典列表加载失败');
    } finally {
      setLoading(false);
    }
  }, [identity]);

  useEffect(() => {
    void load();
  }, [load]);

  const startNew = () => {
    setError('');
    setEditor({ id: '', name: '', rules: [{ ...emptyRule }] });
  };

  const startEdit = (dict: PronunciationDictionary) => {
    setError('');
    setEditor({ id: dict.id, name: dict.name, rules: dict.rules.length ? dict.rules.map((r) => ({ ...r })) : [{ ...emptyRule }] });
  };

  const save = async () => {
    if (!editor) return;
    const name = editor.name.trim();
    const rules = editor.rules.filter((rule) => rule.pattern.trim() && rule.replacement.trim());
    if (!name || rules.length === 0) {
      setError('需填写词典名称，且至少一条有效的「读法→替代发音」规则。');
      return;
    }
    setError('');
    try {
      if (editor.id) {
        await updateDictionary(identity, editor.id, { name, rules });
        setNotice('词典已更新。');
      } else {
        await createDictionary(identity, { name, rules });
        setNotice('词典已创建。');
      }
      setEditor(null);
      void load();
    } catch (err) {
      setError(err instanceof Error ? err.message : '保存失败');
    }
  };

  const onDelete = async (dict: PronunciationDictionary) => {
    if (!window.confirm(`删除词典「${dict.name}」？`)) return;
    setError('');
    try {
      await deleteDictionary(identity, dict.id);
      setNotice('词典已删除。');
      void load();
    } catch (err) {
      setError(err instanceof Error ? err.message : '删除失败');
    }
  };

  const setRule = (index: number, patch: Partial<PronunciationRule>) => {
    setEditor((current) =>
      current
        ? { ...current, rules: current.rules.map((rule, i) => (i === index ? { ...rule, ...patch } : rule)) }
        : current
    );
  };

  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">设置 · 发音词典</span>
          <h1>发音词典</h1>
          <small className="page-sub">
            解决专有名词误读（如 CUDA、MySQL、Kubernetes）。规则按「读法 → 替代发音」在合成前应用到讲稿。
          </small>
        </div>
        <div className="page-actions">
          <button type="button" className="button-primary" onClick={startNew}>
            新建词典
          </button>
          <button type="button" onClick={() => void load()}>
            刷新
          </button>
        </div>
      </section>

      {error && <p className="form-error">{error}</p>}
      {notice && <p className="floating-notice">{notice}</p>}

      {editor && (
        <section className="panel editor-form">
          <header className="table-head">
            <h2>{editor.id ? '编辑词典' : '新建词典'}</h2>
          </header>
          <div className="form-stack">
            <label className="field-label">
              名称
              <input
                value={editor.name}
                placeholder="如 产品专有名词"
                onChange={(e) => setEditor((c) => (c ? { ...c, name: e.currentTarget.value } : c))}
              />
            </label>
            {editor.rules.map((rule, index) => (
              <div key={index} className="dict-rule-row">
                <input value={rule.pattern} placeholder="原文（如 CUDA）" onChange={(e) => setRule(index, { pattern: e.currentTarget.value })} />
                <input value={rule.replacement} placeholder="替代发音（如 库达）" onChange={(e) => setRule(index, { replacement: e.currentTarget.value })} />
                <label className="check-inline">
                  <input type="checkbox" checked={rule.enabled} onChange={(e) => setRule(index, { enabled: e.currentTarget.checked })} />
                  启用
                </label>
                <button type="button" className="danger" onClick={() => setEditor((c) => (c ? { ...c, rules: c.rules.filter((_, i) => i !== index) } : c))}>
                  删除行
                </button>
              </div>
            ))}
            <div className="row-actions">
              <button type="button" onClick={() => setEditor((c) => (c ? { ...c, rules: [...c.rules, { ...emptyRule }] } : c))}>
                添加规则
              </button>
              <button type="button" className="button-primary" onClick={() => void save()}>
                保存
              </button>
              <button type="button" onClick={() => setEditor(null)}>
                取消
              </button>
            </div>
          </div>
        </section>
      )}

      <section className="panel">
        <header className="table-head">
          <h2>词典列表</h2>
        </header>
        {loading ? (
          <p className="empty-state">加载中…</p>
        ) : dictionaries.length === 0 ? (
          <p className="empty-state">暂无词典。新建后就可在合成前生效（更新后合成缓存自动失效）。</p>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>名称</th>
                <th>规则数</th>
                <th>启用规则</th>
                <th className="col-actions">操作</th>
              </tr>
            </thead>
            <tbody>
              {dictionaries.map((dict) => {
                const enabled = dict.rules.filter((rule) => rule.enabled).length;
                return (
                  <tr key={dict.id}>
                    <td>
                      <strong>{dict.name || '(未命名)'}</strong>
                      <small className="cell-sub block-sub">{dict.id}</small>
                    </td>
                    <td>{dict.rules.length}</td>
                    <td>{enabled}</td>
                    <td className="col-actions">
                      <div className="row-actions">
                        <button type="button" onClick={() => startEdit(dict)}>
                          编辑
                        </button>
                        <button type="button" className="danger" onClick={() => void onDelete(dict)}>
                          删除
                        </button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </section>
    </div>
  );
}