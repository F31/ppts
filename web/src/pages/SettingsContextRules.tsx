import { useCallback, useEffect, useState } from 'react';
import { createContextRule, deleteContextRule, listContextRules, updateContextRule, type ClientIdentity } from '../api';
import { useI18n } from '../i18n';
import { useConfirmDialog } from '../components/ConfirmDialog';
import type { ContextRule } from '../types';

type EditorState = {
  id: string; // 空=新建
  pattern: string;
  replacement: string;
  priority: number;
  enabled: boolean;
};

const emptyEditor: EditorState = { id: '', pattern: '', replacement: '', priority: 10, enabled: true };

// SettingsContextRules：上下文替换规则管理（M5 数据驱动，V3.0 §3.3）。
// 每条 = 单行规则（Go 正则 pattern → 产出模板 replacement），优先级小值先执行。
// 与发音词典（整 span 字面替换）互补：contextual_rules 支持捕获组与上下文限定。
export function SettingsContextRules({ identity }: { identity: ClientIdentity }) {
  const { t } = useI18n();
  const confirmDialog = useConfirmDialog();
  const { ask: confirmAsk, dialog: confirmDialogEl } = confirmDialog;
  const [rules, setRules] = useState<ContextRule[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [editor, setEditor] = useState<EditorState | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      setRules(await listContextRules(identity));
    } catch (err) {
      setError(err instanceof Error ? err.message : t('ctx.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [identity, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const startNew = () => {
    setError('');
    setEditor({ ...emptyEditor });
  };

  const startEdit = (rule: ContextRule) => {
    setError('');
    setEditor({ id: rule.id, pattern: rule.pattern, replacement: rule.replacement, priority: rule.priority, enabled: rule.enabled });
  };

  const save = async () => {
    if (!editor) return;
    const pattern = editor.pattern.trim();
    const replacement = editor.replacement.trim();
    if (!pattern || !replacement) {
      setError(t('ctx.invalid'));
      return;
    }
    setError('');
    const payload = { pattern, replacement, priority: editor.priority, enabled: editor.enabled };
    try {
      if (editor.id) {
        await updateContextRule(identity, editor.id, payload);
        setNotice(t('ctx.updated'));
      } else {
        await createContextRule(identity, payload);
        setNotice(t('ctx.created'));
      }
      setEditor(null);
      void load();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('ctx.saveFailed'));
    }
  };

  const onDelete = async (rule: ContextRule) => {
    const ok = await confirmAsk({ kind: 'confirm', titleKey: 'ctx.deleteTitle', messageKey: 'ctx.deleteConfirm', messageValues: { pattern: rule.pattern }, confirmKey: 'common.delete', danger: true });
    if (!ok) return;
    setError('');
    try {
      await deleteContextRule(identity, rule.id);
      setNotice(t('ctx.deleted'));
      void load();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('ctx.deleteFailed'));
    }
  };

  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">{t('ctx.eyebrow')}</span>
          <h1>{t('ctx.title')}</h1>
          <small className="page-sub">{t('ctx.subtitle')}</small>
        </div>
        <div className="page-actions">
          <button type="button" className="button-primary" onClick={startNew}>
            {t('ctx.new')}
          </button>
          <button type="button" onClick={() => void load()}>
            {t('common.refresh')}
          </button>
        </div>
      </section>

      {error && <p className="form-error" role="alert">{error}</p>}
      {notice && <p className="floating-notice" role="status" aria-live="polite">{notice}</p>}

      {editor && (
        <section className="panel editor-form">
          <header className="table-head">
            <h2>{editor.id ? t('ctx.editorEdit') : t('ctx.editorNew')}</h2>
          </header>
          <div className="form-stack">
            <label className="field-label">
              {t('ctx.patternLabel')}
              <input
                value={editor.pattern}
                placeholder={t('ctx.patternPlaceholder')}
                onChange={(e) => setEditor((c) => (c ? { ...c, pattern: e.currentTarget.value } : c))}
              />
              <small className="field-hint">{t('ctx.patternHint')}</small>
            </label>
            <label className="field-label">
              {t('ctx.replacementLabel')}
              <input
                value={editor.replacement}
                placeholder={t('ctx.replacementPlaceholder')}
                onChange={(e) => setEditor((c) => (c ? { ...c, replacement: e.currentTarget.value } : c))}
              />
              <small className="field-hint">{t('ctx.replacementHint')}</small>
            </label>
            <label className="field-label">
              {t('ctx.priorityLabel')}
              <input
                type="number"
                value={editor.priority}
                onChange={(e) => setEditor((c) => (c ? { ...c, priority: Number(e.currentTarget.value) || 0 } : c))}
              />
              <small className="field-hint">{t('ctx.priorityHint')}</small>
            </label>
            <label className="check-inline">
              <input type="checkbox" checked={editor.enabled} onChange={(e) => setEditor((c) => (c ? { ...c, enabled: e.currentTarget.checked } : c))} />
              {t('common.enable')}
            </label>
            <div className="row-actions">
              <button type="button" className="button-primary" onClick={() => void save()}>
                {t('common.save')}
              </button>
              <button type="button" onClick={() => setEditor(null)}>
                {t('common.cancel')}
              </button>
            </div>
          </div>
        </section>
      )}

      <section className="panel">
        <header className="table-head">
          <h2>{t('ctx.listTitle')}</h2>
        </header>
        {loading ? (
          <p className="empty-state">{t('common.loading')}</p>
        ) : rules.length === 0 ? (
          <p className="empty-state">{t('ctx.empty')}</p>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>{t('ctx.colPattern')}</th>
                <th>{t('ctx.colReplacement')}</th>
                <th>{t('ctx.colPriority')}</th>
                <th>{t('ctx.colEnabled')}</th>
                <th className="col-actions">{t('ctx.colActions')}</th>
              </tr>
            </thead>
            <tbody>
              {rules.map((rule) => (
                <tr key={rule.id}>
                  <td>
                    <code>{rule.pattern}</code>
                    <small className="cell-sub block-sub">{rule.id}</small>
                  </td>
                  <td>
                    <code>{rule.replacement}</code>
                  </td>
                  <td>{rule.priority}</td>
                  <td>{rule.enabled ? t('common.yes') : t('common.no')}</td>
                  <td className="col-actions">
                    <div className="row-actions">
                      <button type="button" onClick={() => startEdit(rule)}>
                        {t('common.edit')}
                      </button>
                      <button type="button" className="danger" onClick={() => void onDelete(rule)}>
                        {t('common.delete')}
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
      {confirmDialogEl}
    </div>
  );
}
