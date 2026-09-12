package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	pptsv1 "github.com/F31/ppts/gen/ppts/v1"
	"github.com/F31/ppts/gen/ppts/v1/pptsv1connect"
	"github.com/F31/ppts/internal/app"
	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/upload"
)

// signedURLParser 是本地签名链接解析能力；S3 后端的预签名链接走原生 HTTP，不实现。
type signedURLParser interface {
	ParseSignedURL(raw string) (objectstore.ObjectKey, objectstore.Operation, error)
}

// UploadService 是 UploadService RPC transport，持有 app 用例并映射错误码。
type UploadService struct {
	pptsv1connect.UnimplementedUploadServiceHandler
	svc    *app.UploadService
	parser signedURLParser
}

// NewUploadService 创建 transport。objects 为本地后端时用于把 local:// 链接重写为 HTTP 相对路径。
func NewUploadService(svc *app.UploadService, objects objectstore.ObjectStore) *UploadService {
	parser, _ := objects.(signedURLParser)
	return &UploadService{svc: svc, parser: parser}
}

func (s *UploadService) CreateUpload(ctx context.Context, req *connect.Request[pptsv1.CreateUploadRequest]) (*connect.Response[pptsv1.CreateUploadResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	res, err := s.svc.CreateUpload(ctx, p.TenantID, app.UploadRequest{
		ProjectID: req.Msg.GetProjectId(), Filename: req.Msg.GetFilename(),
		SizeBytes: req.Msg.GetSizeBytes(), ContentType: req.Msg.GetContentType(),
		DeleteSourceAfter: req.Msg.GetDeleteSourceAfter(),
	})
	if err != nil {
		return nil, uploadError(err)
	}
	urls := make([]string, 0, len(res.SignedURLs))
	for _, u := range res.SignedURLs {
		urls = append(urls, s.rewriteURL(u))
	}
	// 单段直传：chunk_size_bytes=0 表示无需分片。
	return connect.NewResponse(&pptsv1.CreateUploadResponse{
		UploadId: res.Session.ID, SignedUploadUrls: urls,
		ChunkSizeBytes: 0, ObjectKey: res.ObjectKey,
	}), nil
}

func (s *UploadService) CompleteUpload(ctx context.Context, req *connect.Request[pptsv1.CompleteUploadRequest]) (*connect.Response[pptsv1.CompleteUploadResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	srcRevID, jobID, err := s.svc.CompleteUpload(ctx, p.TenantID, req.Msg.GetUploadId(), req.Msg.GetExpectedHash(), req.Msg.GetSizeBytes())
	if err != nil {
		return nil, uploadError(err)
	}
	return connect.NewResponse(&pptsv1.CompleteUploadResponse{
		SourceRevisionId: srcRevID, JobId: jobID,
	}), nil
}

func (s *UploadService) AbortUpload(ctx context.Context, req *connect.Request[pptsv1.AbortUploadRequest]) (*connect.Response[pptsv1.AbortUploadResponse], error) {
	p, err := requirePrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.svc.AbortUpload(ctx, p.TenantID, req.Msg.GetUploadId()); err != nil {
		return nil, uploadError(err)
	}
	return connect.NewResponse(&pptsv1.AbortUploadResponse{}), nil
}

// rewriteURL 把 local:// 签名链接重写为同源 HTTP 相对路径（供浏览器直接 PUT/GET）。
// http(s) 预签名链接（S3 等）原样返回。
func (s *UploadService) rewriteURL(signed string) string {
	return rewriteLocalSignedURL(s.parser, signed)
}

// rewriteLocalSignedURL 是各 transport 共用的本地签名链接重写：
// local:// → /ppts/object/{key}?token&op。S3 等原生预签名链接原样返回。
func rewriteLocalSignedURL(parser signedURLParser, signed string) string {
	if parser == nil || !strings.HasPrefix(signed, "local://") {
		return signed
	}
	u, err := url.Parse(signed)
	if err != nil {
		return signed
	}
	key := strings.TrimPrefix(u.Path, "/")
	q := u.Query()
	return "/ppts/object/" + key + "?" + q.Encode()
}

func uploadError(err error) error {
	switch {
	case errors.Is(err, app.ErrUploadInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, app.ErrUploadTooLarge):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, app.ErrUploadSizeMismatch):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, app.ErrUploadHashMismatch):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, app.ErrUploadNotReceived):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, app.ErrUploadAborted):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, upload.ErrAlreadyCompleted):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, upload.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

// signedObjectHandler 提供本地后端的签名对象端点：URL 形如
// /ppts/object/{key}?token=...&op=write|read|delete。
// 令牌即授权（绑定键与操作，短期有效），与 S3 预签名直传语义一致。
type signedObjectHandler struct {
	objects objectstore.ObjectStore
	parser  signedURLParser
}

func (h *signedObjectHandler) serve(methodOp objectstore.Operation, w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	q := r.URL.Query()
	raw := (&url.URL{Scheme: "local", Host: "object", Path: "/" + key,
		RawQuery: url.Values{"token": {q.Get("token")}, "op": {q.Get("op")}}.Encode()}).String()
	k, op, err := h.parser.ParseSignedURL(raw)
	if err != nil || op != methodOp {
		http.Error(w, "invalid or mismatched signed url", http.StatusBadRequest)
		return
	}
	switch op {
	case objectstore.OpWrite:
		body := http.MaxBytesReader(w, r.Body, app.MaxUploadBytes+1<<20)
		if err := h.objects.Put(r.Context(), k, body, objectstore.ObjectMeta{
			ContentType: r.Header.Get("Content-Type"),
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case objectstore.OpRead:
		rc, meta, err := h.objects.Get(r.Context(), k)
		if err != nil {
			if errors.Is(err, objectstore.ErrObjectNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rc.Close()
		if meta.ContentType != "" {
			w.Header().Set("Content-Type", meta.ContentType)
		}
		w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
		_, _ = io.Copy(w, rc)
	case objectstore.OpDelete:
		if err := h.objects.Delete(r.Context(), k); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
