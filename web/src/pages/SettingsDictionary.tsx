import { useCallback, useEffect, useState } from 'react';
import { createDictionary, deleteDictionary, listDictionaries, updateDictionary, type ClientIdentity } from '../api';
import { useI18n } from '../i18n';
import { useConfirmDialog } from '../components/ConfirmDialog';
import type { PronunciationDictionary, PronunciationRule } from '../types';

type EditorState = {
  id: string; // 空=新建
  name: string;
  rules: PronunciationRule[];
};

const emptyRule: PronunciationRule = { pattern: '', replacement: '', enabled: true };

export function SettingsDictionary({ identity }: { identity: ClientIdentity }) {
  const { t } = useI18n();
  const confirmDialog = useConfirmDialog();
  const { ask: confirmAsk, dialog: confirmDialogEl } = confirmDialog;
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
      setError(err instanceof Error ? err.message : t('dict.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [identity, t]);

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
      setError(t('dict.invalid'));
      return;
    }
    setError('');
    try {
      if (editor.id) {
        await updateDictionary(identity, editor.id, { name, rules });
        setNotice(t('dict.updated'));
      } else {
        await createDictionary(identity, { name, rules });
        setNotice(t('dict.created'));
      }
      setEditor(null);
      void load();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('dict.saveFailed'));
    }
  };

  const onDelete = async (dict: PronunciationDictionary) => {
    const ok = await confirmAsk({ kind: 'confirm', titleKey: 'dict.deleteTitle', messageKey: 'dict.deleteConfirm', messageValues: { name: dict.name }, confirmKey: 'common.delete', danger: true });
    if (!ok) return;
    setError('');
    try {
      await deleteDictionary(identity, dict.id);
      setNotice(t('dict.deleted'));
      void load();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('dict.deleteFailed'));
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
          <span className="eyebrow">{t('dict.eyebrow')}</span>
          <h1>{t('dict.title')}</h1>
          <small className="page-sub">{t('dict.subtitle')}</small>
        </div>
        <div className="page-actions">
          <button type="button" className="button-primary" onClick={startNew}>
            {t('dict.new')}
          </button>
          <button type="button" onClick={() => void load()}>
            {t('common.refresh')}
          </button>
        </div>
      </section>

      {error && <p className="form-error">{error}</p>}
      {notice && <p className="floating-notice">{notice}</p>}

      {editor && (
        <section className="panel editor-form">
          <header className="table-head">
            <h2>{editor.id ? t('dict.editorEdit') : t('dict.editorNew')}</h2>
          </header>
          <div className="form-stack">
            <label className="field-label">
              {t('dict.nameLabel')}
              <input
                value={editor.name}
                placeholder={t('dict.namePlaceholder')}
                onChange={(e) => setEditor((c) => (c ? { ...c, name: e.currentTarget.value } : c))}
              />
            </label>
            {editor.rules.map((rule, index) => (
              <div key={index} className="dict-rule-row">
                <input value={rule.pattern} placeholder={t('dict.patternPlaceholder')} onChange={(e) => setRule(index, { pattern: e.currentTarget.value })} />
                <input value={rule.replacement} placeholder={t('dict.replacementPlaceholder')} onChange={(e) => setRule(index, { replacement: e.currentTarget.value })} />
                <label className="check-inline">
                  <input type="checkbox" checked={rule.enabled} onChange={(e) => setRule(index, { enabled: e.currentTarget.checked })} />
                  {t('common.enable')}
                </label>
                <button type="button" className="danger" onClick={() => setEditor((c) => (c ? { ...c, rules: c.rules.filter((_, i) => i !== index) } : c))}>
                  {t('dict.deleteRow')}
                </button>
              </div>
            ))}
            <div className="row-actions">
              <button type="button" onClick={() => setEditor((c) => (c ? { ...c, rules: [...c.rules, { ...emptyRule }] } : c))}>
                {t('dict.addRule')}
              </button>
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
          <h2>{t('dict.listTitle')}</h2>
        </header>
        {loading ? (
          <p className="empty-state">{t('common.loading')}</p>
        ) : dictionaries.length === 0 ? (
          <p className="empty-state">{t('dict.empty')}</p>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>{t('dict.colName')}</th>
                <th>{t('dict.colRules')}</th>
                <th>{t('dict.colEnabled')}</th>
                <th className="col-actions">{t('dict.colActions')}</th>
              </tr>
            </thead>
            <tbody>
              {dictionaries.map((dict) => {
                const enabled = dict.rules.filter((rule) => rule.enabled).length;
                return (
                  <tr key={dict.id}>
                    <td>
                      <strong>{dict.name || t('dict.unnamed')}</strong>
                      <small className="cell-sub block-sub">{dict.id}</small>
                    </td>
                    <td>{dict.rules.length}</td>
                    <td>{enabled}</td>
                    <td className="col-actions">
                      <div className="row-actions">
                        <button type="button" onClick={() => startEdit(dict)}>
                          {t('common.edit')}
                        </button>
                        <button type="button" className="danger" onClick={() => void onDelete(dict)}>
                          {t('common.delete')}
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
      {confirmDialogEl}
    </div>
  );
}