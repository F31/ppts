import { useCallback, useEffect, useState } from 'react';
import { createContextRule, deleteContextRule, listContextRules, updateContextRule, type ClientIdentity } from '../api';
import { useI18n } from '../i18n';
import { useConfirmDialog } from '../components/ConfirmDialog';
import type { ContextRule } from '../types';

type EditorMode = 'simple' | 'advanced';

type EditorState = {
  id: string;
  mode: EditorMode;
  terms: string;
  pattern: string;
  replacement: string;
  priority: number;
  enabled: boolean;
};

type RuleTemplate = {
  key: string;
  terms: string;
  replacement: string;
  sample: string;
};

const templates: RuleTemplate[] = [
  { key: 'brand', terms: 'mindbase, Mindbase', replacement: 'MindBase', sample: 'mindbase 可以统一企业知识。' },
  { key: 'agent', terms: 'AI Agent, 智能体', replacement: 'AI 智能体', sample: '我们的 AI Agent 可以作为业务智能体。' },
  { key: 'model', terms: '大模型', replacement: '大语言模型', sample: '大模型正在重塑知识生产。' },
  { key: 'value', terms: '便宜', replacement: '高性价比', sample: '这套方案更便宜，也更容易落地。' },
  { key: 'customer', terms: '客户', replacement: '企业客户', sample: '客户可以快速完成内容生产。' }
];

const emptyEditor: EditorState = {
  id: '', mode: 'simple', terms: '', pattern: '', replacement: '', priority: 10, enabled: true
};

function escapeRegex(raw: string): string {
  return raw.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function termsToPattern(terms: string): string {
  const parts = terms.split(/[，,\n]/).map((v) => v.trim()).filter(Boolean);
  const uniq = Array.from(new Set(parts));
  if (uniq.length === 0) return '';
  return uniq.map(escapeRegex).join('|');
}

function applyPreview(input: string, pattern: string, replacement: string): string {
  if (!input.trim() || !pattern.trim()) return input;
  try {
    return input.replace(new RegExp(pattern, 'g'), replacement);
  } catch {
    return input;
  }
}

export function SettingsContextRules({ identity }: { identity: ClientIdentity }) {
  const { t } = useI18n();
  const confirmDialog = useConfirmDialog();
  const { ask: confirmAsk, dialog: confirmDialogEl } = confirmDialog;
  const [rules, setRules] = useState<ContextRule[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [editor, setEditor] = useState<EditorState | null>(null);
  const [previewInput, setPreviewInput] = useState('');
  const [showAdvancedList, setShowAdvancedList] = useState(false);

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

  const startNew = (template?: RuleTemplate) => {
    setError('');
    setEditor({
      ...emptyEditor,
      terms: template?.terms ?? '',
      replacement: template?.replacement ?? '',
      pattern: template ? termsToPattern(template.terms) : ''
    });
    setPreviewInput(template?.sample ?? t('ctx.previewSample'));
  };

  const startAdvancedEdit = (rule: ContextRule) => {
    setError('');
    setEditor({ id: rule.id, mode: 'advanced', terms: '', pattern: rule.pattern, replacement: rule.replacement, priority: rule.priority, enabled: rule.enabled });
    setPreviewInput(t('ctx.previewSample'));
  };

  const save = async () => {
    if (!editor) return;
    const pattern = editor.mode === 'simple' ? termsToPattern(editor.terms) : editor.pattern.trim();
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

  const previewPattern = editor ? (editor.mode === 'simple' ? termsToPattern(editor.terms) : editor.pattern.trim()) : '';
  const previewOutput = editor ? applyPreview(previewInput, previewPattern, editor.replacement) : '';

  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">{t('ctx.eyebrow')}</span>
          <h1>{t('ctx.title')}</h1>
          <small className="page-sub">{t('ctx.subtitle')}</small>
        </div>
        <div className="page-actions">
          <button type="button" className="button-primary" onClick={() => startNew()}>{t('ctx.new')}</button>
          <button type="button" onClick={() => void load()}>{t('common.refresh')}</button>
        </div>
      </section>

      <section className="panel ctx-help-panel">
        <strong>{t('ctx.helpTitle')}</strong>
        <p>{t('ctx.helpBody')}</p>
        <p className="field-hint">{t('ctx.pronunciationHint')}</p>
      </section>

      {error && <p className="form-error" role="alert">{error}</p>}
      {notice && <p className="floating-notice" role="status" aria-live="polite">{notice}</p>}

      <section className="panel">
        <header className="table-head">
          <h2>{t('ctx.templatesTitle')}</h2>
        </header>
        <div className="ctx-template-grid">
          {templates.map((tpl) => (
            <button key={tpl.key} type="button" className="ctx-template-card" onClick={() => startNew(tpl)}>
              <strong>{t(`ctx.template.${tpl.key}.title`)}</strong>
              <span>{t(`ctx.template.${tpl.key}.desc`)}</span>
              <code>{tpl.terms} → {tpl.replacement}</code>
            </button>
          ))}
        </div>
      </section>

      {editor && (
        <section className="panel editor-form">
          <header className="table-head">
            <h2>{editor.id ? t('ctx.editorEdit') : t('ctx.editorNew')}</h2>
            <div className="segmented-actions">
              <button type="button" className={editor.mode === 'simple' ? 'active' : ''} onClick={() => setEditor((c) => (c ? { ...c, mode: 'simple', terms: c.terms || c.pattern } : c))}>{t('ctx.simpleMode')}</button>
              <button type="button" className={editor.mode === 'advanced' ? 'active' : ''} onClick={() => setEditor((c) => (c ? { ...c, mode: 'advanced', pattern: c.pattern || termsToPattern(c.terms) } : c))}>{t('ctx.advancedMode')}</button>
            </div>
          </header>
          <div className="form-stack">
            {editor.mode === 'simple' ? (
              <label className="field-label">
                {t('ctx.termsLabel')}
                <textarea rows={3} value={editor.terms} placeholder={t('ctx.termsPlaceholder')} onChange={(e) => setEditor((c) => (c ? { ...c, terms: e.currentTarget.value, pattern: termsToPattern(e.currentTarget.value) } : c))} />
                <small className="field-hint">{t('ctx.termsHint')}</small>
              </label>
            ) : (
              <label className="field-label">
                {t('ctx.patternLabel')}
                <input value={editor.pattern} placeholder={t('ctx.patternPlaceholder')} onChange={(e) => setEditor((c) => (c ? { ...c, pattern: e.currentTarget.value } : c))} />
                <small className="field-hint">{t('ctx.patternHint')}</small>
              </label>
            )}
            <label className="field-label">
              {t('ctx.replacementLabel')}
              <input value={editor.replacement} placeholder={t('ctx.replacementPlaceholder')} onChange={(e) => setEditor((c) => (c ? { ...c, replacement: e.currentTarget.value } : c))} />
              <small className="field-hint">{t('ctx.replacementHint')}</small>
            </label>
            {editor.mode === 'advanced' && (
              <label className="field-label">
                {t('ctx.priorityLabel')}
                <input type="number" value={editor.priority} onChange={(e) => setEditor((c) => (c ? { ...c, priority: Number(e.currentTarget.value) || 0 } : c))} />
                <small className="field-hint">{t('ctx.priorityHint')}</small>
              </label>
            )}
            <label className="check-inline">
              <input type="checkbox" checked={editor.enabled} onChange={(e) => setEditor((c) => (c ? { ...c, enabled: e.currentTarget.checked } : c))} />
              {t('common.enable')}
            </label>
            <div className="ctx-preview-box">
              <label className="field-label">
                {t('ctx.previewInput')}
                <textarea rows={2} value={previewInput} onChange={(e) => setPreviewInput(e.currentTarget.value)} />
              </label>
              <div>
                <span className="field-hint">{t('ctx.previewOutput')}</span>
                <p>{previewOutput || '—'}</p>
              </div>
            </div>
            <div className="row-actions">
              <button type="button" className="button-primary" onClick={() => void save()}>{t('common.save')}</button>
              <button type="button" onClick={() => setEditor(null)}>{t('common.cancel')}</button>
            </div>
          </div>
        </section>
      )}

      <section className="panel">
        <header className="table-head">
          <h2>{t('ctx.listTitle')}</h2>
          <button type="button" onClick={() => setShowAdvancedList((v) => !v)}>{showAdvancedList ? t('ctx.hideAdvanced') : t('ctx.showAdvanced')}</button>
        </header>
        {loading ? (
          <p className="empty-state">{t('common.loading')}</p>
        ) : rules.length === 0 ? (
          <div className="empty-state">
            <p>{t('ctx.empty')}</p>
            <button type="button" className="button-primary" onClick={() => startNew(templates[0])}>{t('ctx.useTemplate')}</button>
          </div>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>{t('ctx.colPattern')}</th>
                <th>{t('ctx.colReplacement')}</th>
                {showAdvancedList && <th>{t('ctx.colPriority')}</th>}
                <th>{t('ctx.colEnabled')}</th>
                <th className="col-actions">{t('ctx.colActions')}</th>
              </tr>
            </thead>
            <tbody>
              {rules.map((rule) => (
                <tr key={rule.id}>
                  <td><code>{rule.pattern}</code>{showAdvancedList && <small className="cell-sub block-sub">{rule.id}</small>}</td>
                  <td><code>{rule.replacement}</code></td>
                  {showAdvancedList && <td>{rule.priority}</td>}
                  <td>{rule.enabled ? t('common.yes') : t('common.no')}</td>
                  <td className="col-actions">
                    <div className="row-actions">
                      <button type="button" onClick={() => startAdvancedEdit(rule)}>{t('common.edit')}</button>
                      <button type="button" className="danger" onClick={() => void onDelete(rule)}>{t('common.delete')}</button>
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
