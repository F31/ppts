package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"
	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/audit"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/membership"
	"github.com/F31/ppts/internal/project"
)

type ProjectService struct {
	pptsv1connect.UnimplementedProjectServiceHandler
	store      project.ProjectStore
	objects    objectstore.ObjectStore
	members    membership.Reader
	audit      audit.Recorder
	slideNotes project.SlideNotesStore
}

func NewProjectService(store project.ProjectStore, objects objectstore.ObjectStore, members membership.Reader, recorder audit.Recorder, slideNotes project.SlideNotesStore) *ProjectService {
	return &ProjectService{store: store, objects: objects, members: members, audit: recorder, slideNotes: slideNotes}
}

func (s *ProjectService) Create(ctx context.Context, req *connect.Request[pptsv1.CreateProjectRequest]) (*connect.Response[pptsv1.CreateProjectResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireRole(ctx, s.members, membership.RoleEditor); err != nil {
		return nil, err
	}
	title := strings.TrimSpace(req.Msg.GetTitle())
	if title == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("title is required"))
	}
	created, err := s.store.CreateProject(ctx, p.TenantID, p.UserID, title)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pptsv1.CreateProjectResponse{Project: toProtoProject(created)}), nil
}

func (s *ProjectService) Get(ctx context.Context, req *connect.Request[pptsv1.GetProjectRequest]) (*connect.Response[pptsv1.Project], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	got, override, err := resolveProject(ctx, s.store, s.members, p.TenantID, p.UserID, req.Msg.GetId())
	if err != nil {
		return nil, projectError(err)
	}
	if override {
		recordAdminOverride(ctx, s.audit, "project.admin_override_access", got.ID, map[string]any{"surface": "rpc.Get"})
	}
	return connect.NewResponse(toProtoProject(got)), nil
}

func (s *ProjectService) List(ctx context.Context, req *connect.Request[pptsv1.ListProjectsRequest]) (*connect.Response[pptsv1.ListProjectsResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	userID, _ := projectAccessUser(ctx, s.members)
	projects, next, err := s.store.ListProjects(ctx, p.TenantID, userID, req.Msg.GetCursor().GetValue(), int(req.Msg.GetPageSize()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	out := make([]*pptsv1.Project, 0, len(projects))
	for _, item := range projects {
		out = append(out, toProtoProject(item))
	}
	return connect.NewResponse(&pptsv1.ListProjectsResponse{Projects: out, NextCursor: &pptsv1.Cursor{Value: next}}), nil
}

func (s *ProjectService) Archive(ctx context.Context, req *connect.Request[pptsv1.ArchiveProjectRequest]) (*connect.Response[pptsv1.Project], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireRole(ctx, s.members, membership.RoleAdmin); err != nil {
		return nil, err
	}
	if _, override, err := resolveProject(ctx, s.store, s.members, p.TenantID, p.UserID, req.Msg.GetId()); err != nil {
		return nil, projectError(err)
	} else if override {
		recordAdminOverride(ctx, s.audit, "project.admin_override_archive", req.Msg.GetId(), map[string]any{"surface": "rpc.Archive"})
	}
	archived, err := s.store.ArchiveProject(ctx, p.TenantID, "", req.Msg.GetId())
	if err != nil {
		return nil, projectError(err)
	}
	return connect.NewResponse(toProtoProject(archived)), nil
}

type shapeText struct {
	Text string `json:"text"`
}

func (s *ProjectService) GetSlides(ctx context.Context, req *connect.Request[pptsv1.GetSlidesRequest]) (*connect.Response[pptsv1.GetSlidesResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(req.Msg.GetProjectId())
	if projectID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id is required"))
	}
	revisionNo := int(req.Msg.GetRevisionNo())
	projectRow, _, err := resolveProject(ctx, s.store, s.members, p.TenantID, p.UserID, projectID)
	if err != nil {
		return nil, projectError(err)
	}
	if revisionNo == 0 {
		revisionNo = projectRow.CurrentRevision
	}
	docKey := objectstore.ObjectKey{
		TenantID: p.TenantID, ProjectID: projectID,
		Revision: srcRevString(revisionNo), AssetType: "document", AssetID: "extracted", Ext: "json",
	}
	rc, _, err := s.objects.Get(ctx, docKey)
	if err != nil {
		if errors.Is(err, objectstore.ErrObjectNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("project has no parsed document yet"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var doc struct {
		Pages []struct {
			Index        int         `json:"index"`
			SlideID      string      `json:"slideId"`
			Name         string      `json:"name"`
			NotesText    string      `json:"notesText"`
			FeatureFlags []string    `json:"featureFlags"`
			Shapes       []shapeText `json:"shapes"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("invalid parsed document: %w", err))
	}
	// 合并用户编辑过的备注（优先级高于解析所得备注）。
	if s.slideNotes != nil {
		if storedNotes, err := s.slideNotes.Get(ctx, p.TenantID, projectID, revisionNo); err == nil && storedNotes != nil {
			stored := storedNotes
			for i := range doc.Pages {
				if n, ok := stored[doc.Pages[i].SlideID]; ok {
					doc.Pages[i].NotesText = n
					doc.Pages[i].FeatureFlags = append(doc.Pages[i].FeatureFlags, "userNotes")
				}
			}
		}
	}
	out := make([]*pptsv1.SlideSummary, 0, len(doc.Pages))
	for _, pg := range doc.Pages {
		out = append(out, &pptsv1.SlideSummary{
			SlideId: pg.SlideID, Index: int32(pg.Index),
			Title: pg.Name, HasNotes: pg.NotesText != "",
			Preview:      previewText(pg.NotesText, pg.Shapes),
			FeatureFlags: pg.FeatureFlags,
		})
	}
	return connect.NewResponse(&pptsv1.GetSlidesResponse{RevisionNo: int64(revisionNo), Slides: out}), nil
}

// previewText 优先用备注，其次首个非空形状文本，截断到 120 字符。
func previewText(notes string, shapes []shapeText) string {
	if notes = strings.TrimSpace(notes); notes != "" {
		return truncate(notes, 120)
	}
	for _, sh := range shapes {
		if t := strings.TrimSpace(sh.Text); t != "" {
			return truncate(t, 120)
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + "…"
}

func srcRevString(n int) string {
	if n < 0 {
		n = 0
	}
	return fmt.Sprintf("src-%02d", n)
}

func (s *ProjectService) CreateSourceRevision(context.Context, *connect.Request[pptsv1.CreateSourceRevisionRequest]) (*connect.Response[pptsv1.SourceRevision], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("CreateSourceRevision is internal to UploadService in G1"))
}

func toProtoProject(p *project.Project) *pptsv1.Project {
	if p == nil {
		return nil
	}
	return &pptsv1.Project{
		Id: p.ID, TenantId: p.TenantID, Owner: p.OwnerUser, Title: p.Title,
		CurrentRevision: int64(p.CurrentRevision), Policy: string(p.Policy), Archived: p.Archived,
		CreatedAtUnix: p.CreatedAt.Unix(),
	}
}

func projectError(err error) error {
	if errors.Is(err, project.ErrProjectNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}
