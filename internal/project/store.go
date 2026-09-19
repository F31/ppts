package project

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/F31/ppts/internal/tenant"
)

// Project 是项目领域实体（V4.0 §7.1）。所有访问都经租户与项目授权。
type Project struct {
	ID                string
	TenantID          string
	OwnerUser         string
	Title             string
	CurrentRevision   int
	Policy            []byte // 租户策略 jsonb
	Archived          bool
	DeleteSourceAfter bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// SourceRevision 是源文件不可变版本（V4.0 §7.1）。
type SourceRevision struct {
	ID            string
	ProjectID     string
	TenantID      string
	RevisionNo    int
	SourceHash    string
	ObjectKey     string
	ParserVersion string
	PageCount     int
	UploadID      string // 上传会话幂等键；非上传路径为空
	CreatedAt     time.Time
}

// Tag 是租户内的项目标签（多对多，#94 标签+分组体系）。
type Tag struct {
	ID        string
	TenantID  string
	Name      string
	Color     string
	CreatedAt time.Time
}

// Folder 是租户内的项目分组（单归属，#94 标签+分组体系）。
type Folder struct {
	ID        string
	TenantID  string
	Name      string
	CreatedBy string
	CreatedAt time.Time
}

// ProjectOrg 是项目与标签/分组的关联视图（#94），供前端合并进项目列表。
type ProjectOrg struct {
	ProjectID string
	FolderID  string // 空 = 未分类
	TagIDs    []string
}

// ErrTagNotFound 表示标签不存在或越权。
var ErrTagNotFound = errors.New("project: tag not found")

// ErrTagNameExists 表示租户内标签名已存在（唯一约束冲突）。
var ErrTagNameExists = errors.New("project: tag name already exists")

// ErrFolderNotFound 表示分组不存在或越权。
var ErrFolderNotFound = errors.New("project: folder not found")

// ErrFolderNameExists 表示租户内分组名已存在（唯一约束冲突）。
var ErrFolderNameExists = errors.New("project: folder name already exists")

// ErrProjectNotFound 表示项目不存在或越权。
var ErrProjectNotFound = errors.New("project: project not found")

// ErrSourceRevisionNotFound 表示源版本不存在。
var ErrSourceRevisionNotFound = errors.New("project: source revision not found")

// ProjectStore 项目与源版本存储端口。
type ProjectStore interface {
	CreateProject(ctx context.Context, tenantID, owner, title string) (*Project, error)
	GetProject(ctx context.Context, tenantID, id string) (*Project, error)
	ListProjects(ctx context.Context, tenantID, cursor string, pageSize int) ([]*Project, string, error)
	ArchiveProject(ctx context.Context, tenantID, id string) (*Project, error)
	// CreateSourceRevision 原子递增 current_revision 并写入不可变源版本。
	CreateSourceRevision(ctx context.Context, tenantID string, in NewSourceRevision) (*SourceRevision, error)
	// GetSourceRevision 按项目与 revision 号查询。
	GetSourceRevision(ctx context.Context, tenantID, projectID string, revisionNo int) (*SourceRevision, error)
	// ListSourceRevisions 列出项目全部未软删的源版本（倒序），供前端版本历史查看。
	ListSourceRevisions(ctx context.Context, tenantID, projectID string) ([]*SourceRevision, error)
	// ---- 标签与分组（#94 标签+分组体系） ----
	// 标签（多对多，租户内 name 唯一）。
	ListTags(ctx context.Context, tenantID string) ([]*Tag, error)
	CreateTag(ctx context.Context, tenantID, name, color string) (*Tag, error)
	RenameTag(ctx context.Context, tenantID, tagID, name, color string) (*Tag, error)
	DeleteTag(ctx context.Context, tenantID, tagID string) error
	AttachTag(ctx context.Context, tenantID, projectID, tagID string) error
	DetachTag(ctx context.Context, tenantID, projectID, tagID string) error
	// 分组（单归属，删分组时项目回落未分类）。
	ListFolders(ctx context.Context, tenantID string) ([]*Folder, error)
	CreateFolder(ctx context.Context, tenantID, name, createdBy string) (*Folder, error)
	RenameFolder(ctx context.Context, tenantID, folderID, name string) (*Folder, error)
	DeleteFolder(ctx context.Context, tenantID, folderID string) error
	MoveProject(ctx context.Context, tenantID, projectID, folderID string) error
	// ListProjectOrganization 返回租户内每个项目的 folder_id 与 tag_id 列表（#94，供前端合并列表）。
	ListProjectOrganization(ctx context.Context, tenantID string) ([]*ProjectOrg, error)
}

// NewSourceRevision 新建源版本的输入。
type NewSourceRevision struct {
	ProjectID     string
	SourceHash    string
	ObjectKey     string
	ParserVersion string
	UploadID      string // 上传链路幂等键；为空时不启用按会话去重
}

// PGProjectStore 以 PostgreSQL 实现 ProjectStore。revision 递增与版本行写入同事务，
// 保证"accept 成功前已持久化"（V4.0 §10.1 API 只有提交成功后才返回已接受）。
type PGProjectStore struct {
	pool *pgxpool.Pool
}

// NewPGProjectStore 创建存储。
func NewPGProjectStore(pool *pgxpool.Pool) *PGProjectStore {
	return &PGProjectStore{pool: pool}
}

func (s *PGProjectStore) CreateProject(ctx context.Context, tenantID, owner, title string) (*Project, error) {
	var p *Project
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		p, e = scanProject(tx.QueryRow(ctx,
			`INSERT INTO projects (id, tenant_id, owner_user, title)
			 VALUES (gen_random_uuid(), $1, $2, $3) RETURNING id, tenant_id, owner_user, title,
			   current_revision, policy, archived, delete_source_after, created_at, updated_at`,
			tenantID, owner, title))
		return e
	})
	return p, err
}

func (s *PGProjectStore) GetProject(ctx context.Context, tenantID, id string) (*Project, error) {
	var p *Project
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		p, e = scanProject(tx.QueryRow(ctx,
			`SELECT id, tenant_id, owner_user, title, current_revision, policy, archived,
			   delete_source_after, created_at, updated_at
			 FROM projects WHERE id=$1 AND tenant_id=$2`, id, tenantID))
		return e
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
	return p, err
}

func (s *PGProjectStore) ListProjects(ctx context.Context, tenantID, cursor string, pageSize int) ([]*Project, string, error) {
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 50
	}
	var projects []*Project
	next := ""
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var rows pgx.Rows
		var err error
		if cursor == "" {
			rows, err = tx.Query(ctx,
				`SELECT id, tenant_id, owner_user, title, current_revision, policy, archived,
				   delete_source_after, created_at, updated_at
				 FROM projects WHERE tenant_id=$1 AND archived=false
				 ORDER BY created_at DESC LIMIT $2`, tenantID, pageSize+1)
		} else {
			createdBefore, parseErr := time.Parse(time.RFC3339Nano, cursor)
			if parseErr != nil {
				return parseErr
			}
			rows, err = tx.Query(ctx,
				`SELECT id, tenant_id, owner_user, title, current_revision, policy, archived,
				   delete_source_after, created_at, updated_at
				 FROM projects WHERE tenant_id=$1 AND archived=false AND created_at < $2
				 ORDER BY created_at DESC LIMIT $3`, tenantID, createdBefore, pageSize+1)
		}
		if err != nil {
			return err
		}
		defer rows.Close()
		projects = make([]*Project, 0, pageSize)
		for rows.Next() {
			p, err := scanProject(rows)
			if err != nil {
				return err
			}
			projects = append(projects, p)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(projects) > pageSize {
			next = projects[pageSize-1].CreatedAt.Format(time.RFC3339Nano)
			projects = projects[:pageSize]
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return projects, next, nil
}

func (s *PGProjectStore) ArchiveProject(ctx context.Context, tenantID, id string) (*Project, error) {
	var p *Project
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		p, e = scanProject(tx.QueryRow(ctx,
			`UPDATE projects SET archived=true, updated_at=now()
			 WHERE id=$1 AND tenant_id=$2
			 RETURNING id, tenant_id, owner_user, title, current_revision, policy, archived,
			   delete_source_after, created_at, updated_at`, id, tenantID))
		return e
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
	return p, err
}

func (s *PGProjectStore) CreateSourceRevision(ctx context.Context, tenantID string, in NewSourceRevision) (*SourceRevision, error) {
	var sr *SourceRevision
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		// 锁定项目行，串行化 current_revision 递增。
		var current int
		if err := tx.QueryRow(ctx,
			`SELECT current_revision FROM projects WHERE id=$1 AND tenant_id=$2 FOR UPDATE`,
			in.ProjectID, tenantID).Scan(&current); err != nil {
			return errors.Join(ErrProjectNotFound, err)
		}

		// 上传链路幂等：同一 upload_id 已建源版本时返回既有版本，不重复递增。
		if in.UploadID != "" {
			existing, err := scanSourceRevision(tx.QueryRow(ctx,
				`SELECT id, project_id, tenant_id, revision_no, source_hash, object_key, parser_version, page_count, upload_id, created_at
				 FROM source_revisions WHERE tenant_id=$1 AND upload_id=$2`,
				tenantID, in.UploadID))
			switch {
			case err == nil:
				sr = existing
				return nil
			case !errors.Is(err, ErrSourceRevisionNotFound):
				return err
			}
		}

		sr = &SourceRevision{
			ProjectID: in.ProjectID, TenantID: tenantID, RevisionNo: current + 1,
			SourceHash: in.SourceHash, ObjectKey: in.ObjectKey, ParserVersion: in.ParserVersion,
			UploadID: in.UploadID,
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO source_revisions (id, project_id, tenant_id, revision_no, source_hash, object_key, parser_version, upload_id)
			 VALUES (gen_random_uuid(), $1,$2,$3,$4,$5,$6,$7)
			 RETURNING id, created_at`,
			sr.ProjectID, sr.TenantID, sr.RevisionNo, sr.SourceHash, sr.ObjectKey, sr.ParserVersion, sr.UploadID,
		).Scan(&sr.ID, &sr.CreatedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE projects SET current_revision=$3, updated_at=now() WHERE id=$1 AND tenant_id=$2`,
			in.ProjectID, tenantID, sr.RevisionNo); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sr, nil
}

func (s *PGProjectStore) GetSourceRevision(ctx context.Context, tenantID, projectID string, revisionNo int) (*SourceRevision, error) {
	var r *SourceRevision
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		r, e = scanSourceRevision(tx.QueryRow(ctx,
			`SELECT id, project_id, tenant_id, revision_no, source_hash, object_key, parser_version, page_count, upload_id, created_at
			 FROM source_revisions WHERE project_id=$1 AND tenant_id=$2 AND revision_no=$3`,
			projectID, tenantID, revisionNo))
		return e
	})
	return r, err
}

func (s *PGProjectStore) ListSourceRevisions(ctx context.Context, tenantID, projectID string) ([]*SourceRevision, error) {
	var revs []*SourceRevision
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx,
			`SELECT id, project_id, tenant_id, revision_no, source_hash, object_key, parser_version, page_count, upload_id, created_at
			 FROM source_revisions WHERE project_id=$1 AND tenant_id=$2 AND source_deleted_at IS NULL
			 ORDER BY revision_no DESC`,
			projectID, tenantID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var r SourceRevision
			if e := rows.Scan(&r.ID, &r.ProjectID, &r.TenantID, &r.RevisionNo, &r.SourceHash,
				&r.ObjectKey, &r.ParserVersion, &r.PageCount, &r.UploadID, &r.CreatedAt); e != nil {
				return e
			}
			revs = append(revs, &r)
		}
		return rows.Err()
	})
	return revs, err
}

func nullIfEmpty(s string) interface{} {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// ---- 标签与分组（#94 标签+分组体系） ----

func (s *PGProjectStore) ListTags(ctx context.Context, tenantID string) ([]*Tag, error) {
	var tags []*Tag
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx,
			`SELECT id, tenant_id, name, color, created_at FROM tags WHERE tenant_id=$1 ORDER BY name`,
			tenantID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var t Tag
			if e := rows.Scan(&t.ID, &t.TenantID, &t.Name, &t.Color, &t.CreatedAt); e != nil {
				return e
			}
			tags = append(tags, &t)
		}
		return rows.Err()
	})
	return tags, err
}

func (s *PGProjectStore) CreateTag(ctx context.Context, tenantID, name, color string) (*Tag, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("project: tag name required")
	}
	var t *Tag
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var exists int
		if e := tx.QueryRow(ctx, `SELECT 1 FROM tags WHERE tenant_id=$1 AND name=$2`, tenantID, name).Scan(&exists); e == nil {
			return ErrTagNameExists
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		t = &Tag{}
		return tx.QueryRow(ctx,
			`INSERT INTO tags (tenant_id, name, color) VALUES ($1,$2,$3) RETURNING id, tenant_id, name, color, created_at`,
			tenantID, name, nullIfEmpty(color)).Scan(&t.ID, &t.TenantID, &t.Name, &t.Color, &t.CreatedAt)
	})
	return t, err
}

func (s *PGProjectStore) RenameTag(ctx context.Context, tenantID, tagID, name, color string) (*Tag, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("project: tag name required")
	}
	var t *Tag
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var exists int
		if e := tx.QueryRow(ctx, `SELECT 1 FROM tags WHERE tenant_id=$1 AND name=$2 AND id<>$3`, tenantID, name, tagID).Scan(&exists); e == nil {
			return ErrTagNameExists
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		t = &Tag{}
		e := tx.QueryRow(ctx,
			`UPDATE tags SET name=$3, color=$4 WHERE id=$2 AND tenant_id=$1 RETURNING id, tenant_id, name, color, created_at`,
			tenantID, tagID, name, nullIfEmpty(color)).Scan(&t.ID, &t.TenantID, &t.Name, &t.Color, &t.CreatedAt)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrTagNotFound
		}
		return e
	})
	return t, err
}

func (s *PGProjectStore) DeleteTag(ctx context.Context, tenantID, tagID string) error {
	return tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `DELETE FROM tags WHERE id=$1 AND tenant_id=$2`, tagID, tenantID)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			return ErrTagNotFound
		}
		return nil
	})
}

func (s *PGProjectStore) AttachTag(ctx context.Context, tenantID, projectID, tagID string) error {
	return tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.getProjectRow(ctx, tx, tenantID, projectID); e != nil {
			return e
		}
		var tid string
		if e := tx.QueryRow(ctx, `SELECT id FROM tags WHERE id=$1 AND tenant_id=$2`, tagID, tenantID).Scan(&tid); e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return ErrTagNotFound
			}
			return e
		}
		_, e := tx.Exec(ctx,
			`INSERT INTO project_tags (tenant_id, project_id, tag_id) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`,
			tenantID, projectID, tagID)
		return e
	})
}

func (s *PGProjectStore) DetachTag(ctx context.Context, tenantID, projectID, tagID string) error {
	return tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx,
			`DELETE FROM project_tags WHERE tenant_id=$1 AND project_id=$2 AND tag_id=$3`,
			tenantID, projectID, tagID)
		return e
	})
}

func (s *PGProjectStore) ListFolders(ctx context.Context, tenantID string) ([]*Folder, error) {
	var folders []*Folder
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx,
			`SELECT id, tenant_id, name, created_by, created_at FROM folders WHERE tenant_id=$1 ORDER BY name`,
			tenantID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var f Folder
			if e := rows.Scan(&f.ID, &f.TenantID, &f.Name, &f.CreatedBy, &f.CreatedAt); e != nil {
				return e
			}
			folders = append(folders, &f)
		}
		return rows.Err()
	})
	return folders, err
}

func (s *PGProjectStore) CreateFolder(ctx context.Context, tenantID, name, createdBy string) (*Folder, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("project: folder name required")
	}
	var f *Folder
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var exists int
		if e := tx.QueryRow(ctx, `SELECT 1 FROM folders WHERE tenant_id=$1 AND name=$2`, tenantID, name).Scan(&exists); e == nil {
			return ErrFolderNameExists
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		f = &Folder{}
		return tx.QueryRow(ctx,
			`INSERT INTO folders (tenant_id, name, created_by) VALUES ($1,$2,$3) RETURNING id, tenant_id, name, created_by, created_at`,
			tenantID, name, nullIfEmpty(createdBy)).Scan(&f.ID, &f.TenantID, &f.Name, &f.CreatedBy, &f.CreatedAt)
	})
	return f, err
}

func (s *PGProjectStore) RenameFolder(ctx context.Context, tenantID, folderID, name string) (*Folder, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("project: folder name required")
	}
	var f *Folder
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var exists int
		if e := tx.QueryRow(ctx, `SELECT 1 FROM folders WHERE tenant_id=$1 AND name=$2 AND id<>$3`, tenantID, name, folderID).Scan(&exists); e == nil {
			return ErrFolderNameExists
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		f = &Folder{}
		e := tx.QueryRow(ctx,
			`UPDATE folders SET name=$3 WHERE id=$2 AND tenant_id=$1 RETURNING id, tenant_id, name, created_by, created_at`,
			tenantID, folderID, name).Scan(&f.ID, &f.TenantID, &f.Name, &f.CreatedBy, &f.CreatedAt)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrFolderNotFound
		}
		return e
	})
	return f, err
}

func (s *PGProjectStore) DeleteFolder(ctx context.Context, tenantID, folderID string) error {
	return tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		// 删除分组前先把项目回落未分类（folder_id SET NULL）。
		if _, e := tx.Exec(ctx,
			`UPDATE projects SET folder_id=NULL, updated_at=now() WHERE tenant_id=$1 AND folder_id=$2`,
			tenantID, folderID); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `DELETE FROM folders WHERE id=$1 AND tenant_id=$2`, folderID, tenantID)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			return ErrFolderNotFound
		}
		return nil
	})
}

func (s *PGProjectStore) MoveProject(ctx context.Context, tenantID, projectID, folderID string) error {
	return tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := s.getProjectRow(ctx, tx, tenantID, projectID); e != nil {
			return e
		}
		if folderID != "" {
			var fid string
			if e := tx.QueryRow(ctx, `SELECT id FROM folders WHERE id=$1 AND tenant_id=$2`, folderID, tenantID).Scan(&fid); e != nil {
				if errors.Is(e, pgx.ErrNoRows) {
					return ErrFolderNotFound
				}
				return e
			}
		}
		_, e := tx.Exec(ctx,
			`UPDATE projects SET folder_id=NULLIF($3,'')::uuid, updated_at=now() WHERE id=$2 AND tenant_id=$1`,
			tenantID, projectID, folderID)
		return e
	})
}

// getProjectRow 在事务内按租户+id 取项目行，不存在返回 ErrProjectNotFound（供标签/分组写操作校验归属）。
func (s *PGProjectStore) getProjectRow(ctx context.Context, tx pgx.Tx, tenantID, projectID string) (*Project, error) {
	p, e := scanProject(tx.QueryRow(ctx,
		`SELECT id, tenant_id, owner_user, title, current_revision, policy, archived, delete_source_after, created_at, updated_at
		 FROM projects WHERE id=$1 AND tenant_id=$2`, projectID, tenantID))
	if errors.Is(e, ErrProjectNotFound) {
		return nil, ErrProjectNotFound
	}
	return p, e
}

// ListProjectOrganization 一次性返回租户内所有项目的 folder_id 与 tag_id 列表（#94）。
// 用 string_agg 聚合 tag id 为逗号串，避免 pgx 扫描 uuid 数组的兼容性差异；空聚合返回空串（无标签）。
func (s *PGProjectStore) ListProjectOrganization(ctx context.Context, tenantID string) ([]*ProjectOrg, error) {
	var orgs []*ProjectOrg
	err := tenant.Run(ctx, s.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx,
			`SELECT p.id::text, p.folder_id::text,
			        COALESCE(string_agg(t.id::text, ',' ORDER BY t.name), '')
			 FROM projects p
			 LEFT JOIN project_tags pt ON pt.project_id = p.id
			 LEFT JOIN tags t ON t.id = pt.tag_id AND t.tenant_id = $1
			 WHERE p.tenant_id = $1
			 GROUP BY p.id, p.folder_id`,
			tenantID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var pid, folderID, tagIDs string
			if e := rows.Scan(&pid, &folderID, &tagIDs); e != nil {
				return e
			}
			o := &ProjectOrg{ProjectID: pid, FolderID: folderID}
			if strings.TrimSpace(tagIDs) != "" {
				for _, x := range strings.Split(tagIDs, ",") {
					if x != "" {
						o.TagIDs = append(o.TagIDs, x)
					}
				}
			}
			orgs = append(orgs, o)
		}
		return rows.Err()
	})
	return orgs, err
}

func scanProject(row pgx.Row) (*Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.TenantID, &p.OwnerUser, &p.Title, &p.CurrentRevision,
		&p.Policy, &p.Archived, &p.DeleteSourceAfter, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func scanSourceRevision(row pgx.Row) (*SourceRevision, error) {
	var r SourceRevision
	err := row.Scan(&r.ID, &r.ProjectID, &r.TenantID, &r.RevisionNo, &r.SourceHash,
		&r.ObjectKey, &r.ParserVersion, &r.PageCount, &r.UploadID, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSourceRevisionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}
