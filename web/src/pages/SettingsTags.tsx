import { useCallback, useEffect, useState } from 'react';
import {
  ConnectError,
  createFolder,
  createTag,
  deleteFolder,
  deleteTag,
  listFolders,
  listTags,
  renameFolder,
  renameTag,
  type ClientIdentity
} from '../api';
import { useI18n } from '../i18n';
import { useConfirmDialog } from '../components/ConfirmDialog';
import type { Folder, Tag } from '../types';

// 标签色板（前端建议色；后端仅存字符串，空 = 默认灰）。
const PALETTE = ['#2563eb', '#16a34a', '#d97706', '#dc2626', '#7c3aed', '#0891b2', '#db2777', '#4b5563'];

export function SettingsTags({ identity }: { identity: ClientIdentity }) {
  const { t } = useI18n();
  const confirmDialog = useConfirmDialog();
  const { ask: confirmAsk, dialog: confirmDialogEl } = confirmDialog;
  const [tags, setTags] = useState<Tag[]>([]);
  const [folders, setFolders] = useState<Folder[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');

  const refresh = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [tagList, folderList] = await Promise.all([listTags(identity), listFolders(identity)]);
      setTags(tagList);
      setFolders(folderList);
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [identity, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // ---- 标签 ----
  const [newTagName, setNewTagName] = useState('');
  const [newTagColor, setNewTagColor] = useState(PALETTE[0]);

  const createTagNow = async () => {
    const name = newTagName.trim();
    if (!name) return;
    try {
      await createTag(identity, name, newTagColor);
      setNewTagName('');
      setNotice(t('tags.created', { name }));
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('tags.createFailed'));
    }
  };

  const renameTagNow = async (tag: Tag) => {
    const name = await confirmAsk({ kind: 'prompt', titleKey: 'tags.renameTitle', initial: tag.name, confirmKey: 'common.save' });
    if (name === null) return;
    const trimmed = name.trim();
    if (!trimmed || trimmed === tag.name) return;
    try {
      await renameTag(identity, tag.id, trimmed, tag.color ?? '');
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('tags.renameFailed'));
    }
  };

  const deleteTagNow = async (tag: Tag) => {
    const ok = await confirmAsk({ kind: 'confirm', titleKey: 'tags.deleteTitle', messageKey: 'tags.deleteConfirm', messageValues: { name: tag.name }, confirmKey: 'common.delete', danger: true });
    if (!ok) return;
    try {
      await deleteTag(identity, tag.id);
      setNotice(t('tags.deleted', { name: tag.name }));
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('tags.deleteFailed'));
    }
  };

  // ---- 分组 ----
  const [newFolderName, setNewFolderName] = useState('');

  const createFolderNow = async () => {
    const name = newFolderName.trim();
    if (!name) return;
    try {
      await createFolder(identity, name);
      setNewFolderName('');
      setNotice(t('folders.created', { name }));
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('folders.createFailed'));
    }
  };

  const renameFolderNow = async (folder: Folder) => {
    const name = await confirmAsk({ kind: 'prompt', titleKey: 'folders.renameTitle', initial: folder.name, confirmKey: 'common.save' });
    if (name === null) return;
    const trimmed = name.trim();
    if (!trimmed || trimmed === folder.name) return;
    try {
      await renameFolder(identity, folder.id, trimmed);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('folders.renameFailed'));
    }
  };

  const deleteFolderNow = async (folder: Folder) => {
    const ok = await confirmAsk({ kind: 'confirm', titleKey: 'folders.deleteTitle', messageKey: 'folders.deleteConfirm', messageValues: { name: folder.name }, confirmKey: 'common.delete', danger: true });
    if (!ok) return;
    try {
      await deleteFolder(identity, folder.id);
      setNotice(t('folders.deleted', { name: folder.name }));
      await refresh();
    } catch (err) {
      // 后端在分组下还有项目时返回 412 FailedPrecondition（err.folderNotEmpty）。
      if (err instanceof ConnectError && err.code === 'http_412') {
        setError(t('folders.deleteBlocked'));
      } else {
        setError(err instanceof Error ? err.message : t('folders.deleteFailed'));
      }
    }
  };

  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">{t('settings.title')}</span>
          <h1>{t('tags.title')}</h1>
        </div>
      </section>

      {error && <p className="form-error" role="alert">{error}</p>}
      {notice && <p className="floating-notice" role="status" aria-live="polite">{notice}</p>}

      {loading ? (
        <p className="empty-state">{t('common.loading')}</p>
      ) : (
        <div className="tags-admin">
          <section className="panel">
            <header className="table-head">
              <h2>{t('tags.listTitle')}</h2>
              <div className="create-inline">
                <input
                  value={newTagName}
                  placeholder={t('tags.newName')}
                  onChange={(e) => setNewTagName(e.currentTarget.value)}
                  aria-label={t('tags.newName')}
                />
                <div className="color-palette" role="group" aria-label={t('tags.color')}>
                  {PALETTE.map((c) => (
                    <button
                      key={c}
                      type="button"
                      className={`swatch ${newTagColor === c ? 'active' : ''}`}
                      style={{ background: c }}
                      onClick={() => setNewTagColor(c)}
                      aria-label={c}
                    />
                  ))}
                </div>
                <button type="button" onClick={() => void createTagNow()} disabled={!newTagName.trim()}>
                  {t('tags.create')}
                </button>
              </div>
            </header>
            {tags.length === 0 ? (
              <p className="empty-state">{t('tags.empty')}</p>
            ) : (
              <ul className="tag-admin-list">
                {tags.map((tag) => (
                  <li key={tag.id} className="tag-admin-item">
                    <span className="tag-chip" style={{ background: tag.color || '#4b5563' }}>
                      {tag.name}
                    </span>
                    <button type="button" onClick={() => void renameTagNow(tag)}>
                      {t('common.rename')}
                    </button>
                    <button type="button" className="danger" onClick={() => void deleteTagNow(tag)}>
                      {t('common.delete')}
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section className="panel">
            <header className="table-head">
              <h2>{t('folders.listTitle')}</h2>
              <div className="create-inline">
                <input
                  value={newFolderName}
                  placeholder={t('folders.newName')}
                  onChange={(e) => setNewFolderName(e.currentTarget.value)}
                  aria-label={t('folders.newName')}
                />
                <button type="button" onClick={() => void createFolderNow()} disabled={!newFolderName.trim()}>
                  {t('folders.create')}
                </button>
              </div>
            </header>
            {folders.length === 0 ? (
              <p className="empty-state">{t('folders.empty')}</p>
            ) : (
              <ul className="tag-admin-list">
                {folders.map((folder) => (
                  <li key={folder.id} className="tag-admin-item">
                    <span className="folder-name">📁 {folder.name}</span>
                    <button type="button" onClick={() => void renameFolderNow(folder)}>
                      {t('common.rename')}
                    </button>
                    <button type="button" className="danger" onClick={() => void deleteFolderNow(folder)}>
                      {t('common.delete')}
                    </button>
                  </li>
                ))}
              </ul>
            )}
            <p className="hint-note">{t('folders.deleteHint')}</p>
          </section>
        </div>
      )}
      {confirmDialogEl}
    </div>
  );
}
