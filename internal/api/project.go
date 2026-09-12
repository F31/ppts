package api

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/project"
)

type ProjectService struct {
	pptsv1connect.UnimplementedProjectServiceHandler
	store project.ProjectStore
}

func NewProjectService(store project.ProjectStore) *ProjectService {
	return &ProjectService{store: store}
}

func (s *ProjectService) Create(ctx context.Context, req *connect.Request[pptsv1.CreateProjectRequest]) (*connect.Response[pptsv1.CreateProjectResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
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
	got, err := s.store.GetProject(ctx, p.TenantID, req.Msg.GetId())
	if err != nil {
		return nil, projectError(err)
	}
	return connect.NewResponse(toProtoProject(got)), nil
}

func (s *ProjectService) List(ctx context.Context, req *connect.Request[pptsv1.ListProjectsRequest]) (*connect.Response[pptsv1.ListProjectsResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	projects, next, err := s.store.ListProjects(ctx, p.TenantID, req.Msg.GetCursor().GetValue(), int(req.Msg.GetPageSize()))
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
	archived, err := s.store.ArchiveProject(ctx, p.TenantID, req.Msg.GetId())
	if err != nil {
		return nil, projectError(err)
	}
	return connect.NewResponse(toProtoProject(archived)), nil
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
