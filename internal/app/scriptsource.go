package app

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ScriptSourceKind 是用户为"无备注页"显式选择的讲稿来源（驱动草稿生成文本来源）。
type ScriptSourceKind string

const (
	// ScriptSourceLayout 版式正文：全部形状文本（默认行为）。
	ScriptSourceLayout ScriptSourceKind = "layout"
	// ScriptSourceTitle 仅标题形状。
	ScriptSourceTitle ScriptSourceKind = "title"
	// ScriptSourceBody 仅正文（排除标题形状）。
	ScriptSourceBody ScriptSourceKind = "body"
	// ScriptSourceNotes 仅演讲者备注。
	ScriptSourceNotes ScriptSourceKind = "notes"
	// ScriptSourceCustom 自定义文本（CustomText 携带）。
	ScriptSourceCustom ScriptSourceKind = "custom"
)

// ValidScriptSourceKinds 用于入参校验。
var ValidScriptSourceKinds = map[ScriptSourceKind]bool{
	ScriptSourceLayout: true,
	ScriptSourceTitle:  true,
	ScriptSourceBody:   true,
	ScriptSourceNotes:  true,
	ScriptSourceCustom: true,
}

// ScriptSourceChoice 是单页来源选择。
type ScriptSourceChoice struct {
	SlideID    string
	Kind       ScriptSourceKind
	CustomText string
}

// ScriptSourceStore 持久化每页讲稿来源选择（无备注页显式指定驱动草稿来源）。
type ScriptSourceStore interface {
	Set(ctx context.Context, tenantID, projectID, slideID string, kind ScriptSourceKind, customText string) error
	List(ctx context.Context, tenantID, projectID string) (map[string]ScriptSourceChoice, error)
}

type scriptSourcePGStore struct {
	pool *pgxpool.Pool
}

// NewScriptSourceStore 创建基于 pgxpool 的来源存储。
func NewScriptSourceStore(pool *pgxpool.Pool) ScriptSourceStore {
	return &scriptSourcePGStore{pool: pool}
}

func (s *scriptSourcePGStore) Set(ctx context.Context, tenantID, projectID, slideID string, kind ScriptSourceKind, customText string) error {
	if tenantID == "" || projectID == "" || slideID == "" {
		return errors.New("script_source: tenant/project/slide id required")
	}
	if !ValidScriptSourceKinds[kind] {
		return errors.New("script_source: invalid kind")
	}
	if kind != ScriptSourceCustom {
		customText = ""
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO slide_script_sources (tenant_id, project_id, slide_id, source, custom_text, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (tenant_id, project_id, slide_id)
		DO UPDATE SET source = EXCLUDED.source, custom_text = EXCLUDED.custom_text, updated_at = now()
	`, tenantID, projectID, slideID, string(kind), customText)
	return err
}

func (s *scriptSourcePGStore) List(ctx context.Context, tenantID, projectID string) (map[string]ScriptSourceChoice, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT slide_id, source, custom_text FROM slide_script_sources
		WHERE tenant_id = $1 AND project_id = $2
	`, tenantID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ScriptSourceChoice{}
	for rows.Next() {
		var slideID, source, custom string
		if err := rows.Scan(&slideID, &source, &custom); err != nil {
			return nil, err
		}
		out[slideID] = ScriptSourceChoice{SlideID: slideID, Kind: ScriptSourceKind(source), CustomText: custom}
	}
	return out, rows.Err()
}
