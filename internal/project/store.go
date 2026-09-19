package project

import (
	"context"
	"errors"
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
