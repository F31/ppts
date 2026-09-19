package project

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/F31/ppts/internal/integrations/objectstore"
)

// SlideNotesStore 持久化单页演讲者备注（手写覆盖解析所得备注）。
// 备注以对象存储侧载文件保存：{tenant}/{project}/{revisionNo}/notes.json，
// key = slideId，value = 备注文本。
type SlideNotesStore interface {
	// Get 读取指定修订版本下所有幻灯片备注；不存在时返回 nil。
	Get(ctx context.Context, tenantID, projectID string, revisionNo int) (map[string]string, error)
	// Set 保存单页备注；empty 时删除该页备注。
	Set(ctx context.Context, tenantID, projectID string, revisionNo int, slideID, notes string) error
}

// slideNotesObjectKey 构造备注侧载文件路径。
func slideNotesObjectKey(tenantID, projectID string, revisionNo int) objectstore.ObjectKey {
	return objectstore.ObjectKey{
		TenantID: tenantID, ProjectID: projectID,
		Revision: fmt.Sprintf("%d", revisionNo), AssetType: "notes", AssetID: "", Ext: "json",
	}
}

// PgSlideNotesStore 基于对象存储实现 SlideNotesStore。
type PgSlideNotesStore struct{ objects objectstore.ObjectStore }

// NewPgSlideNotesStore 创建基于对象存储的备注持久化实现。
func NewPgSlideNotesStore(objects objectstore.ObjectStore) SlideNotesStore {
	return &PgSlideNotesStore{objects: objects}
}

func (s *PgSlideNotesStore) Get(ctx context.Context, tenantID, projectID string, revisionNo int) (map[string]string, error) {
	key := slideNotesObjectKey(tenantID, projectID, revisionNo)
	rc, _, err := s.objects.Get(ctx, key)
	if err != nil {
		if errors.Is(err, objectstore.ErrObjectNotFound) {
			return nil, nil
		}
		return nil, err
	}
	defer rc.Close()
	var m map[string]string
	if err := json.NewDecoder(rc).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode notes: %w", err)
	}
	return m, nil
}

func (s *PgSlideNotesStore) Set(ctx context.Context, tenantID, projectID string, revisionNo int, slideID, notes string) error {
	key := slideNotesObjectKey(tenantID, projectID, revisionNo)
	m, err := s.Get(ctx, tenantID, projectID, revisionNo)
	if err != nil {
		return err
	}
	if m == nil {
		m = make(map[string]string)
	}
	if strings.TrimSpace(notes) == "" {
		delete(m, slideID)
	} else {
		m[slideID] = notes
	}
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal notes: %w", err)
	}
	if err := s.objects.Put(ctx, key, bytes.NewReader(data), objectstore.ObjectMeta{ContentType: "application/json"}); err != nil {
		return fmt.Errorf("put notes: %w", err)
	}
	return nil
}
