package project

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
)

// 双存储契约测试。
//
// SQLite 与 PostgreSQL 是两套独立实现（各 34 个方法，各写一份 SQL）。没有任何编译器
// 或类型系统能保证它们语义一致——接口只约束签名，不约束「查不到时返回哪个错误」。
// 此前已经漂移出六条可证实的差异（docs/优化方案-2026-11-08.md §3.1）：run_at 零值、
// 读侧索引、唯一性检查、错误码、写操作校验、归档游标。每一条的症状都一样：
// 换个部署方式，同一段业务代码走进另一条分支；而两种部署的测试各写各的，谁也发现不了。
//
// 这个文件只放**行为断言**：不出现 SQL、不假设方言、不关心表名与列类型。
// 两个实现各自准备环境（SQLite 用内置本地身份，PG 需要真实的 tenants/users 行），
// 用例本身完全相同。以后换掉 SQLite 或加第三个实现，先让这套用例通过，再谈别的。
//
// 判据的可信度来自变异验证：把任意一条已修的差异改回去，这里必须变红。
// 如果某个用例在两种实现上都通过、却对实现差异不敏感，那它只是重复了各自的单测。

type conformanceEnv struct {
	Store       ProjectStore
	Tenant      string
	Owner       string
	Other       string // 非协作者用户
	OtherTenant string // 另一个租户
	// NewUser 返回一个可用于协作者的用户 ID（PG 的 project_collaborators.user_id 有外键）。
	NewUser func(t *testing.T) string
}

// runProjectStoreConformance 是两个实现共用的用例集。
// 每个子用例拿一份独立环境：这些用例会归档、删标签、改协作者，共享状态会让失败互相污染。
func runProjectStoreConformance(t *testing.T, newEnv func(t *testing.T) conformanceEnv) {
	ctx := context.Background()
	cases := []struct {
		name string
		fn   func(*testing.T, context.Context, conformanceEnv)
	}{
		{"ProjectVisibility", conformanceProjectVisibility},
		{"ArchiveRoundTrip", conformanceArchiveRoundTrip},
		{"ActiveListPaging", conformanceActiveListPaging},
		{"ArchivedListPaging", conformanceArchivedListPaging},
		{"SourceRevisionLifecycle", conformanceSourceRevisionLifecycle},
		{"TagLifecycle", conformanceTagLifecycle},
		{"FolderLifecycle", conformanceFolderLifecycle},
		{"CollaborationLifecycle", conformanceCollaborationLifecycle},
		{"ShareLinkLifecycle", conformanceShareLinkLifecycle},
		{"ProjectOrganization", conformanceProjectOrganization},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.fn(t, ctx, newEnv(t))
		})
	}
}

func mustCreateProject(t *testing.T, env conformanceEnv, ctx context.Context, title string) *Project {
	t.Helper()
	p, err := env.Store.CreateProject(ctx, env.Tenant, env.Owner, title)
	if err != nil {
		t.Fatalf("CreateProject(%q): %v", title, err)
	}
	return p
}

func wantErrIs(t *testing.T, err, want error, what string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: err = %v, want %v", what, err, want)
	}
}

// pageAll 用游标把列表走完，返回每一页的行数与全部行。
// 页数上限是为了让「游标没有推进」这类 bug 以「翻了太多页」的形式失败，
// 而不是把测试挂死——归档列表此前正是每页都返回同一批、游标恒为空。
func pageAll(t *testing.T, pageSize int, fetch func(cursor string) ([]*Project, string, error)) []*Project {
	t.Helper()
	var all []*Project
	cursor := ""
	for i := 0; i < 20; i++ {
		items, next, err := fetch(cursor)
		if err != nil {
			t.Fatalf("第 %d 页: %v", i+1, err)
		}
		if len(items) > pageSize {
			t.Fatalf("第 %d 页返回 %d 条，超过 pageSize=%d", i+1, len(items), pageSize)
		}
		all = append(all, items...)
		if next == "" {
			return all
		}
		if next == cursor {
			t.Fatalf("游标没有推进（连续两次都是 %q），列表会无限重复", next)
		}
		cursor = next
	}
	t.Fatalf("翻了 20 页仍没有结束，项目数只有 %d", len(all))
	return nil
}

// assertPartition 断言翻页结果恰好等于期望集合：不重、不漏。
func assertPartition(t *testing.T, got []*Project, wantIDs []string) {
	t.Helper()
	seen := make(map[string]int, len(got))
	gotIDs := make([]string, 0, len(got))
	for _, p := range got {
		seen[p.ID]++
		gotIDs = append(gotIDs, p.ID)
	}
	for id, n := range seen {
		if n > 1 {
			t.Fatalf("项目 %s 在翻页结果里出现 %d 次（重复行）", id, n)
		}
	}
	sort.Strings(gotIDs)
	want := append([]string(nil), wantIDs...)
	sort.Strings(want)
	if len(gotIDs) != len(want) {
		t.Fatalf("翻页拿到 %d 条，期望 %d 条（丢行或多行）\n got=%v\nwant=%v", len(gotIDs), len(want), gotIDs, want)
	}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Fatalf("翻页结果与期望集合不一致\n got=%v\nwant=%v", gotIDs, want)
		}
	}
}

func conformanceProjectVisibility(t *testing.T, ctx context.Context, env conformanceEnv) {
	p := mustCreateProject(t, env, ctx, "季度汇报")
	if _, err := uuid.Parse(p.ID); err != nil {
		t.Fatalf("项目 ID 不是 UUID：%q", p.ID)
	}
	if p.TenantID != env.Tenant || p.OwnerUser != env.Owner || p.Archived || p.CurrentRevision != 0 {
		t.Fatalf("新建项目的字段不对：%+v", p)
	}

	// owner 可见；非协作者不可见（两条路径都必须是「找不到」，不能泄露存在性）。
	if got, err := env.Store.GetProject(ctx, env.Tenant, env.Owner, p.ID); err != nil || got.ID != p.ID {
		t.Fatalf("owner 取项目：%v %+v", err, got)
	}
	_, err := env.Store.GetProject(ctx, env.Tenant, env.Other, p.ID)
	wantErrIs(t, err, ErrProjectNotFound, "非协作者取项目")

	// 非法 ID 是参数错误，不是「不存在」：PG 的 uuid 列会抛 22P02（→500），
	// SQLite 的 TEXT 列则会安静地查不到。两种都不对，统一成 ErrInvalidProjectID。
	_, err = env.Store.GetProject(ctx, env.Tenant, env.Owner, "not-a-uuid")
	wantErrIs(t, err, ErrInvalidProjectID, "非法项目 ID")

	_, err = env.Store.GetProject(ctx, env.Tenant, env.Owner, uuid.NewString())
	wantErrIs(t, err, ErrProjectNotFound, "不存在的项目")

	_, err = env.Store.GetProject(ctx, env.OtherTenant, env.Owner, p.ID)
	wantErrIs(t, err, ErrProjectNotFound, "跨租户取项目")

	list, next, err := env.Store.ListProjects(ctx, env.Tenant, env.Owner, "", 50)
	if err != nil || len(list) != 1 || next != "" {
		t.Fatalf("owner 列表：n=%d next=%q err=%v", len(list), next, err)
	}
	if list, _, err := env.Store.ListProjects(ctx, env.Tenant, env.Other, "", 50); err != nil || len(list) != 0 {
		t.Fatalf("非协作者列表：n=%d err=%v", len(list), err)
	}

	// 坏游标是参数错误，不是 500，也不能被当成「从头开始」。
	if _, _, err := env.Store.ListProjects(ctx, env.Tenant, env.Owner, "not-a-cursor", 50); err == nil {
		t.Fatal("坏游标没有报错")
	} else {
		wantErrIs(t, err, ErrInvalidCursor, "坏游标")
	}
}

func conformanceArchiveRoundTrip(t *testing.T, ctx context.Context, env conformanceEnv) {
	p := mustCreateProject(t, env, ctx, "待归档")

	// 非成员不能归档（ACL 与列表一致）。
	_, err := env.Store.ArchiveProject(ctx, env.Tenant, env.Other, env.Other, p.ID)
	wantErrIs(t, err, ErrProjectNotFound, "非成员归档")

	archived, err := env.Store.ArchiveProject(ctx, env.Tenant, env.Owner, env.Other, p.ID)
	if err != nil {
		t.Fatalf("归档：%v", err)
	}
	if !archived.Archived || archived.ArchivedAt.IsZero() || archived.ArchivedBy != env.Other {
		t.Fatalf("归档结果不对：%+v", archived)
	}
	if archived.ArchivedAt.Before(time.Now().Add(-time.Hour)) {
		t.Fatalf("归档时间离谱：%v", archived.ArchivedAt)
	}

	active, _, err := env.Store.ListProjects(ctx, env.Tenant, env.Owner, "", 50)
	if err != nil || len(active) != 0 {
		t.Fatalf("归档后仍在活跃列表：n=%d err=%v", len(active), err)
	}
	back, next, err := env.Store.ListArchivedProjects(ctx, env.Tenant, env.Owner, "", 50)
	if err != nil || len(back) != 1 || next != "" {
		t.Fatalf("归档列表：n=%d next=%q err=%v", len(back), next, err)
	}
	if back[0].ID != p.ID || back[0].ArchivedAt.IsZero() {
		t.Fatalf("归档列表行的字段不对：%+v", back[0])
	}

	restored, err := env.Store.UnarchiveProject(ctx, env.Tenant, env.Owner, p.ID)
	if err != nil {
		t.Fatalf("取消归档：%v", err)
	}
	if restored.Archived || !restored.ArchivedAt.IsZero() || restored.ArchivedBy != "" {
		t.Fatalf("取消归档后归档元信息没清干净：%+v", restored)
	}
	if back, _, err := env.Store.ListArchivedProjects(ctx, env.Tenant, env.Owner, "", 50); err != nil || len(back) != 0 {
		t.Fatalf("取消归档后仍在归档列表：n=%d err=%v", len(back), err)
	}

	_, err = env.Store.ArchiveProject(ctx, env.Tenant, env.Owner, env.Owner, uuid.NewString())
	wantErrIs(t, err, ErrProjectNotFound, "归档不存在的项目")
}

// conformanceActiveListPaging 与 conformanceArchivedListPaging 守的是同一个性质：
// 游标翻页必须把集合**恰好**切成若干页。
//
// 两个用例都不做 sleep。它们刻意制造「同一时刻创建/归档」：SQLite 的时间列只有
// 毫秒精度，连续写必然撞同一毫秒。只按时间做 keyset 时，这一批会被整体跳过——
// 而它恰好是用户刚批量导入的那批文件。游标带 id 次键才不会丢。
func conformanceActiveListPaging(t *testing.T, ctx context.Context, env conformanceEnv) {
	const n = 5
	wantIDs := make([]string, 0, n)
	for i := 0; i < n; i++ {
		wantIDs = append(wantIDs, mustCreateProject(t, env, ctx, "批量导入").ID)
	}
	got := pageAll(t, 2, func(cursor string) ([]*Project, string, error) {
		return env.Store.ListProjects(ctx, env.Tenant, env.Owner, cursor, 2)
	})
	assertPartition(t, got, wantIDs)
}

func conformanceArchivedListPaging(t *testing.T, ctx context.Context, env conformanceEnv) {
	const n = 5
	wantIDs := make([]string, 0, n)
	for i := 0; i < n; i++ {
		p := mustCreateProject(t, env, ctx, "批量归档")
		if _, err := env.Store.ArchiveProject(ctx, env.Tenant, env.Owner, env.Owner, p.ID); err != nil {
			t.Fatalf("归档 %s: %v", p.ID, err)
		}
		wantIDs = append(wantIDs, p.ID)
	}
	got := pageAll(t, 2, func(cursor string) ([]*Project, string, error) {
		return env.Store.ListArchivedProjects(ctx, env.Tenant, env.Owner, cursor, 2)
	})
	assertPartition(t, got, wantIDs)
}

func conformanceSourceRevisionLifecycle(t *testing.T, ctx context.Context, env conformanceEnv) {
	p := mustCreateProject(t, env, ctx, "版本")

	rev := func(name, uploadID string) *SourceRevision {
		t.Helper()
		sr, err := env.Store.CreateSourceRevision(ctx, env.Tenant, NewSourceRevision{
			ProjectID: p.ID, SourceHash: "hash-" + name, ObjectKey: env.Tenant + "/" + name,
			ParserVersion: "v1", UploadID: uploadID, Filename: name,
		})
		if err != nil {
			t.Fatalf("CreateSourceRevision(%s): %v", name, err)
		}
		return sr
	}

	r1 := rev("a.pptx", "")
	if r1.RevisionNo != 1 || r1.DisplayName != "a.pptx" {
		t.Fatalf("首个版本：%+v", r1)
	}
	if got, _ := env.Store.GetProject(ctx, env.Tenant, env.Owner, p.ID); got.CurrentRevision != 1 {
		t.Fatalf("current_revision = %d, want 1", got.CurrentRevision)
	}

	// 同一 upload_id 重复提交必须返回既有版本，而不是再涨一个号。
	dup1, err := env.Store.CreateSourceRevision(ctx, env.Tenant, NewSourceRevision{
		ProjectID: p.ID, SourceHash: "hash-b", ObjectKey: env.Tenant + "/b.pptx",
		ParserVersion: "v1", UploadID: "upload-42", Filename: "b.pptx",
	})
	if err != nil {
		t.Fatalf("带 upload_id 建版本：%v", err)
	}
	if dup1.RevisionNo != 2 {
		t.Fatalf("第二个版本号 = %d, want 2", dup1.RevisionNo)
	}
	dup2, err := env.Store.CreateSourceRevision(ctx, env.Tenant, NewSourceRevision{
		ProjectID: p.ID, SourceHash: "hash-b", ObjectKey: env.Tenant + "/b.pptx",
		ParserVersion: "v1", UploadID: "upload-42", Filename: "b.pptx",
	})
	if err != nil || dup2.ID != dup1.ID || dup2.RevisionNo != dup1.RevisionNo {
		t.Fatalf("upload_id 幂等失效：first=%+v second=%+v err=%v", dup1, dup2, err)
	}

	if _, err := env.Store.GetSourceRevision(ctx, env.Tenant, p.ID, 99); !errors.Is(err, ErrSourceRevisionNotFound) {
		t.Fatalf("取不存在的版本：%v", err)
	}
	if err := env.Store.UpdateSourceRevisionPageCount(ctx, env.Tenant, p.ID, 2, 12); err != nil {
		t.Fatalf("写回页数：%v", err)
	}
	if got, _ := env.Store.GetSourceRevision(ctx, env.Tenant, p.ID, 2); got.PageCount != 12 {
		t.Fatalf("页数没写回：%+v", got)
	}
	if err := env.Store.UpdateSourceRevisionDisplayName(ctx, env.Tenant, p.ID, 2, "自定义名"); err != nil {
		t.Fatalf("写显示名：%v", err)
	}
	if got, _ := env.Store.GetSourceRevision(ctx, env.Tenant, p.ID, 2); got.DisplayName != "自定义名" {
		t.Fatalf("显示名没写回：%+v", got)
	}

	list, err := env.Store.ListSourceRevisions(ctx, env.Tenant, p.ID)
	if err != nil || len(list) != 2 || list[0].RevisionNo != 2 || list[1].RevisionNo != 1 {
		t.Fatalf("版本列表应为 [2,1]：%+v err=%v", list, err)
	}

	// 删当前版本 → 回退到剩余最新；版本号不复用（MAX+1）。
	if err := env.Store.DeleteSourceRevision(ctx, env.Tenant, p.ID, 2); err != nil {
		t.Fatalf("删当前版本：%v", err)
	}
	if got, _ := env.Store.GetProject(ctx, env.Tenant, env.Owner, p.ID); got.CurrentRevision != 1 {
		t.Fatalf("回退后 current_revision = %d, want 1", got.CurrentRevision)
	}
	if r3 := rev("c.pptx", ""); r3.RevisionNo != 3 {
		t.Fatalf("删除后新版本号 = %d, want 3（不复用 2）", r3.RevisionNo)
	}

	// 重复删除非当前版本是幂等的（导出重试会重复下发删除）。
	if err := env.Store.DeleteSourceRevision(ctx, env.Tenant, p.ID, 1); err != nil {
		t.Fatalf("删 v1：%v", err)
	}
	if err := env.Store.DeleteSourceRevision(ctx, env.Tenant, p.ID, 1); err != nil {
		t.Fatalf("重复删 v1 应当幂等：%v", err)
	}
	if err := env.Store.DeleteSourceRevision(ctx, env.Tenant, p.ID, 3); !errors.Is(err, ErrDeleteLastRevision) {
		t.Fatalf("删最后一个版本：%v, want ErrDeleteLastRevision", err)
	}
}

func conformanceTagLifecycle(t *testing.T, ctx context.Context, env conformanceEnv) {
	tag, err := env.Store.CreateTag(ctx, env.Tenant, "  季度  ", "#fff")
	if err != nil {
		t.Fatalf("建标签：%v", err)
	}
	if tag.Name != "季度" {
		t.Fatalf("标签名没 trim：%q", tag.Name)
	}

	// 重名（含仅空格差异）→ ErrTagNameExists。
	if _, err := env.Store.CreateTag(ctx, env.Tenant, "季度", ""); !errors.Is(err, ErrTagNameExists) {
		t.Fatalf("重名标签：%v, want ErrTagNameExists", err)
	}
	if _, err := env.Store.CreateTag(ctx, env.Tenant, "  季度 ", ""); !errors.Is(err, ErrTagNameExists) {
		t.Fatalf("trim 后重名：%v, want ErrTagNameExists", err)
	}
	// 空名是参数错误。SQLite 以前会真的建出一个空名标签（第二次才报「已存在」），
	// PG 以前是裸 errors.New → 500。
	if _, err := env.Store.CreateTag(ctx, env.Tenant, "   ", ""); !errors.Is(err, ErrTagNameRequired) {
		t.Fatalf("空标签名：%v, want ErrTagNameRequired", err)
	}

	other, err := env.Store.CreateTag(ctx, env.Tenant, "其它", "")
	if err != nil {
		t.Fatalf("建第二个标签：%v", err)
	}
	// 列表要经得起「有标签没配色」——color 是可空列，扫描侧偷懒会整页 500。
	tags, err := env.Store.ListTags(ctx, env.Tenant)
	if err != nil || len(tags) != 2 {
		t.Fatalf("标签列表：n=%d err=%v", len(tags), err)
	}
	for _, got := range tags {
		if got.ID == other.ID && got.Color != "" {
			t.Fatalf("无配色标签的 color 应为空：%+v", got)
		}
		if got.ID == tag.ID && got.Color != "#fff" {
			t.Fatalf("标签配色丢失：%+v", got)
		}
	}
	if _, err := env.Store.RenameTag(ctx, env.Tenant, tag.ID, "其它", ""); !errors.Is(err, ErrTagNameExists) {
		t.Fatalf("重命名撞名：%v, want ErrTagNameExists", err)
	}
	if _, err := env.Store.RenameTag(ctx, env.Tenant, tag.ID, " ", ""); !errors.Is(err, ErrTagNameRequired) {
		t.Fatalf("重命名成空名：%v, want ErrTagNameRequired", err)
	}
	if _, err := env.Store.RenameTag(ctx, env.Tenant, uuid.NewString(), "x", ""); !errors.Is(err, ErrTagNotFound) {
		t.Fatalf("重命名不存在的标签：%v, want ErrTagNotFound", err)
	}
	if _, err := env.Store.RenameTag(ctx, env.Tenant, tag.ID, "季度", "#000"); err != nil {
		t.Fatalf("重命名：%v", err)
	}

	// 跨租户的标签不许贴到自己项目上：SQLite 的 project_tags.tag_id 没有外键，
	// 以前这条路径直接写成功；PG 靠先查租户拦住。
	foreign, err := env.Store.CreateTag(ctx, env.OtherTenant, "别家的", "")
	if err != nil {
		t.Fatalf("在另一个租户建标签：%v", err)
	}
	p := mustCreateProject(t, env, ctx, "贴标签")
	if err := env.Store.AttachTag(ctx, env.Tenant, p.ID, foreign.ID); !errors.Is(err, ErrTagNotFound) {
		t.Fatalf("贴跨租户标签：%v, want ErrTagNotFound", err)
	}
	if err := env.Store.AttachTag(ctx, env.Tenant, p.ID, uuid.NewString()); !errors.Is(err, ErrTagNotFound) {
		t.Fatalf("贴不存在的标签：%v, want ErrTagNotFound", err)
	}
	if err := env.Store.AttachTag(ctx, env.Tenant, uuid.NewString(), tag.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("贴到不存在的项目：%v, want ErrProjectNotFound", err)
	}
	if err := env.Store.AttachTag(ctx, env.Tenant, p.ID, tag.ID); err != nil {
		t.Fatalf("贴标签：%v", err)
	}
	if err := env.Store.AttachTag(ctx, env.Tenant, p.ID, tag.ID); err != nil {
		t.Fatalf("重复贴标签应当幂等：%v", err)
	}
	if org := mustOrg(t, env, ctx, p.ID); len(org.TagIDs) != 1 || org.TagIDs[0] != tag.ID {
		t.Fatalf("贴标签后组织视图：%+v", org)
	}
	if err := env.Store.DetachTag(ctx, env.Tenant, p.ID, tag.ID); err != nil {
		t.Fatalf("摘标签：%v", err)
	}
	if org := mustOrg(t, env, ctx, p.ID); len(org.TagIDs) != 0 {
		t.Fatalf("摘标签后组织视图：%+v", org)
	}

	if err := env.Store.DeleteTag(ctx, env.Tenant, uuid.NewString()); !errors.Is(err, ErrTagNotFound) {
		t.Fatalf("删不存在的标签：%v", err)
	}
	if err := env.Store.DeleteTag(ctx, env.Tenant, other.ID); err != nil {
		t.Fatalf("删标签：%v", err)
	}
}

func conformanceFolderLifecycle(t *testing.T, ctx context.Context, env conformanceEnv) {
	a, err := env.Store.CreateFolder(ctx, env.Tenant, " 分组A ", env.Owner, "")
	if err != nil {
		t.Fatalf("建分组：%v", err)
	}
	if a.Name != "分组A" || a.SortOrder != 0 || a.CreatedBy != env.Owner {
		t.Fatalf("新建分组字段不对：%+v", a)
	}
	b, err := env.Store.CreateFolder(ctx, env.Tenant, "分组B", env.Owner, "")
	if err != nil || b.SortOrder != 1 {
		t.Fatalf("第二个分组 sort_order = %d err=%v, want 1", b.SortOrder, err)
	}
	if _, err := env.Store.CreateFolder(ctx, env.Tenant, "分组A", env.Owner, ""); !errors.Is(err, ErrFolderNameExists) {
		t.Fatalf("重名分组：%v, want ErrFolderNameExists", err)
	}
	if _, err := env.Store.CreateFolder(ctx, env.Tenant, "  ", env.Owner, ""); !errors.Is(err, ErrFolderNameRequired) {
		t.Fatalf("空分组名：%v, want ErrFolderNameRequired", err)
	}
	if _, err := env.Store.CreateFolder(ctx, env.Tenant, "分组C", env.Owner, uuid.NewString()); !errors.Is(err, ErrFolderNotFound) {
		t.Fatalf("插到不存在的分组之后：%v, want ErrFolderNotFound", err)
	}
	// 插到 A 之后：新分组占 A+1，B 顺延。
	c, err := env.Store.CreateFolder(ctx, env.Tenant, "分组C", env.Owner, a.ID)
	if err != nil || c.SortOrder != a.SortOrder+1 {
		t.Fatalf("插队后的 sort_order = %d err=%v", c.SortOrder, err)
	}
	folders, err := env.Store.ListFolders(ctx, env.Tenant)
	if err != nil || len(folders) != 3 {
		t.Fatalf("分组列表：n=%d err=%v", len(folders), err)
	}
	if folders[0].ID != a.ID || folders[1].ID != c.ID || folders[2].ID != b.ID {
		t.Fatalf("分组顺序应为 [A,C,B]：%+v", folders)
	}

	if _, err := env.Store.RenameFolder(ctx, env.Tenant, uuid.NewString(), "x"); !errors.Is(err, ErrFolderNotFound) {
		t.Fatalf("重命名不存在的分组：%v, want ErrFolderNotFound", err)
	}
	if _, err := env.Store.RenameFolder(ctx, env.Tenant, a.ID, "分组B"); !errors.Is(err, ErrFolderNameExists) {
		t.Fatalf("重命名撞名：%v, want ErrFolderNameExists", err)
	}
	if _, err := env.Store.RenameFolder(ctx, env.Tenant, a.ID, " "); !errors.Is(err, ErrFolderNameRequired) {
		t.Fatalf("重命名成空名：%v, want ErrFolderNameRequired", err)
	}

	p := mustCreateProject(t, env, ctx, "移动我")
	if err := env.Store.MoveProject(ctx, env.Tenant, p.ID, a.ID); err != nil {
		t.Fatalf("移动到分组：%v", err)
	}
	if org := mustOrg(t, env, ctx, p.ID); org.FolderID != a.ID {
		t.Fatalf("移动后 folder_id = %q, want %q", org.FolderID, a.ID)
	}
	foreign, err := env.Store.CreateFolder(ctx, env.OtherTenant, "别家的分组", env.Owner, "")
	if err != nil {
		t.Fatalf("另一个租户建分组：%v", err)
	}
	if err := env.Store.MoveProject(ctx, env.Tenant, p.ID, foreign.ID); !errors.Is(err, ErrFolderNotFound) {
		t.Fatalf("移到别的租户的分组：%v, want ErrFolderNotFound", err)
	}
	if err := env.Store.MoveProject(ctx, env.Tenant, uuid.NewString(), a.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("移动不存在的项目：%v, want ErrProjectNotFound", err)
	}

	// 非空分组不能删；清空后可以删；重复删报找不到（不能静默 200）。
	if err := env.Store.DeleteFolder(ctx, env.Tenant, a.ID); !errors.Is(err, ErrFolderNotEmpty) {
		t.Fatalf("删非空分组：%v, want ErrFolderNotEmpty", err)
	}
	if err := env.Store.MoveProject(ctx, env.Tenant, p.ID, ""); err != nil {
		t.Fatalf("移出分组：%v", err)
	}
	if org := mustOrg(t, env, ctx, p.ID); org.FolderID != "" {
		t.Fatalf("移出后 folder_id = %q, want 空", org.FolderID)
	}
	if err := env.Store.DeleteFolder(ctx, env.Tenant, a.ID); err != nil {
		t.Fatalf("删空分组：%v", err)
	}
	if err := env.Store.DeleteFolder(ctx, env.Tenant, a.ID); !errors.Is(err, ErrFolderNotFound) {
		t.Fatalf("重复删分组：%v, want ErrFolderNotFound", err)
	}

	// created_by 可空（系统建组、导入迁移都没有「谁建的」）。列表要能扫出来，不能整页 500。
	if _, err := env.Store.CreateFolder(ctx, env.Tenant, "匿名创建", "", ""); err != nil {
		t.Fatalf("不带 created_by 建分组：%v", err)
	}
	all, err := env.Store.ListFolders(ctx, env.Tenant)
	if err != nil {
		t.Fatalf("分组列表：%v", err)
	}
	var anon *Folder
	for _, f := range all {
		if f.Name == "匿名创建" {
			anon = f
		}
	}
	if anon == nil || anon.CreatedBy != "" {
		t.Fatalf("空 created_by 的分组：%+v", anon)
	}
}

func conformanceCollaborationLifecycle(t *testing.T, ctx context.Context, env conformanceEnv) {
	p := mustCreateProject(t, env, ctx, "协作")

	list, err := env.Store.ListCollaborators(ctx, env.Tenant, p.ID)
	if err != nil || len(list) != 1 || list[0].UserID != env.Owner || list[0].Role != CollabRoleAdmin {
		t.Fatalf("新建项目的协作者应只有 owner(admin)：%+v err=%v", list, err)
	}

	guest := env.NewUser(t)
	c, err := env.Store.InviteCollaborator(ctx, env.Tenant, p.ID, guest, CollabRoleEditor, env.Owner)
	if err != nil || c.Role != CollabRoleEditor || c.UserID != guest {
		t.Fatalf("邀请协作者：%+v err=%v", c, err)
	}
	if _, err := env.Store.GetProject(ctx, env.Tenant, guest, p.ID); err != nil {
		t.Fatalf("协作者应能看到项目：%v", err)
	}
	// 重复邀请是 upsert，不是报错。
	if _, err := env.Store.InviteCollaborator(ctx, env.Tenant, p.ID, guest, CollabRoleViewer, env.Owner); err != nil {
		t.Fatalf("重复邀请（改角色）：%v", err)
	}
	if list, _ := env.Store.ListCollaborators(ctx, env.Tenant, p.ID); len(list) != 2 {
		t.Fatalf("协作者数量 = %d, want 2", len(list))
	}
	if _, err := env.Store.InviteCollaborator(ctx, env.Tenant, p.ID, guest, "owner", env.Owner); !errors.Is(err, ErrInvalidCollaboratorRole) {
		t.Fatalf("非法角色：%v, want ErrInvalidCollaboratorRole", err)
	}
	if _, err := env.Store.InviteCollaborator(ctx, env.Tenant, uuid.NewString(), guest, CollabRoleViewer, env.Owner); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("向不存在的项目邀请：%v, want ErrProjectNotFound", err)
	}

	if _, err := env.Store.UpdateCollaboratorRole(ctx, env.Tenant, p.ID, guest, CollabRoleEditor); err != nil {
		t.Fatalf("改角色：%v", err)
	}
	if _, err := env.Store.UpdateCollaboratorRole(ctx, env.Tenant, p.ID, uuid.NewString(), CollabRoleEditor); !errors.Is(err, ErrCollaboratorNotFound) {
		t.Fatalf("改不存在成员的角色：%v, want ErrCollaboratorNotFound", err)
	}
	if err := env.Store.RemoveCollaborator(ctx, env.Tenant, p.ID, guest); err != nil {
		t.Fatalf("移除协作者：%v", err)
	}
	if _, err := env.Store.GetProject(ctx, env.Tenant, guest, p.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("移除后仍可见：%v", err)
	}
	if err := env.Store.RemoveCollaborator(ctx, env.Tenant, p.ID, guest); !errors.Is(err, ErrCollaboratorNotFound) {
		t.Fatalf("重复移除：%v, want ErrCollaboratorNotFound", err)
	}
}

func conformanceShareLinkLifecycle(t *testing.T, ctx context.Context, env conformanceEnv) {
	p := mustCreateProject(t, env, ctx, "分享")

	l, err := env.Store.CreateShareLink(ctx, env.Tenant, p.ID, ShareAccessViewOnly, "", env.Owner, nil)
	if err != nil {
		t.Fatalf("建分享链接：%v", err)
	}
	if l.Token == "" || l.PasswordProtected || l.Revoked || l.ExpiresAt != nil {
		t.Fatalf("分享链接字段不对：%+v", l)
	}

	// 口令保护：只回「有没有设」的布尔，散列单独取。
	protected, err := env.Store.CreateShareLink(ctx, env.Tenant, p.ID, ShareAccessViewAndComment, "bcrypt-hash", env.Owner, nil)
	if err != nil || !protected.PasswordProtected || protected.PasswordHash != "" {
		t.Fatalf("带口令的链接：%+v err=%v", protected, err)
	}
	if hash, err := env.Store.ShareLinkPasswordHash(ctx, env.Tenant, protected.ID); err != nil || hash != "bcrypt-hash" {
		t.Fatalf("取口令散列：%q err=%v", hash, err)
	}

	// 非法访问模式是参数错误。SQLite 以前复用 ErrShareLinkNotFound（链接还没建就说找不到），
	// PG 是裸 errors.New（→500）。
	if _, err := env.Store.CreateShareLink(ctx, env.Tenant, p.ID, "public", "", env.Owner, nil); !errors.Is(err, ErrInvalidShareAccessMode) {
		t.Fatalf("非法访问模式：%v, want ErrInvalidShareAccessMode", err)
	}
	if _, err := env.Store.CreateShareLink(ctx, env.Tenant, uuid.NewString(), ShareAccessViewOnly, "", env.Owner, nil); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("给不存在的项目建链接：%v, want ErrProjectNotFound", err)
	}

	// 匿名取：命中未撤回未过期；未知/已撤回/已过期一律 ErrShareLinkNotFound（防枚举）。
	if got, err := env.Store.GetShareLinkByToken(ctx, l.Token); err != nil || got.ID != l.ID {
		t.Fatalf("按 token 取：%+v err=%v", got, err)
	}
	if _, err := env.Store.GetShareLinkByToken(ctx, "no-such-token"); !errors.Is(err, ErrShareLinkNotFound) {
		t.Fatalf("未知 token：%v, want ErrShareLinkNotFound", err)
	}
	past := time.Now().Add(-time.Hour)
	expired, err := env.Store.CreateShareLink(ctx, env.Tenant, p.ID, ShareAccessViewOnly, "", env.Owner, &past)
	if err != nil {
		t.Fatalf("建过期链接：%v", err)
	}
	if _, err := env.Store.GetShareLinkByToken(ctx, expired.Token); !errors.Is(err, ErrShareLinkNotFound) {
		t.Fatalf("已过期 token：%v, want ErrShareLinkNotFound", err)
	}

	if err := env.Store.TouchShareLinkAccess(ctx, env.Tenant, l.ID); err != nil {
		t.Fatalf("记录访问：%v", err)
	}
	if err := env.Store.TouchShareLinkAccess(ctx, env.Tenant, uuid.NewString()); err != nil {
		t.Fatalf("记录不存在链接的访问应当是 no-op：%v", err)
	}

	if err := env.Store.RevokeShareLink(ctx, env.Tenant, l.ID); err != nil {
		t.Fatalf("撤回：%v", err)
	}
	if _, err := env.Store.GetShareLinkByToken(ctx, l.Token); !errors.Is(err, ErrShareLinkNotFound) {
		t.Fatalf("撤回后按 token 取：%v, want ErrShareLinkNotFound", err)
	}
	// 重复撤回是幂等的（把状态置成已撤回，不是「删除一条记录」）；
	// 但撤回一个不存在的链接必须报找不到，否则界面上「撤回」永远成功。
	if err := env.Store.RevokeShareLink(ctx, env.Tenant, l.ID); err != nil {
		t.Fatalf("重复撤回应当幂等：%v", err)
	}
	if err := env.Store.RevokeShareLink(ctx, env.Tenant, uuid.NewString()); !errors.Is(err, ErrShareLinkNotFound) {
		t.Fatalf("撤回不存在的链接：%v, want ErrShareLinkNotFound", err)
	}
	if _, err := env.Store.ShareLinkPasswordHash(ctx, env.Tenant, uuid.NewString()); !errors.Is(err, ErrShareLinkNotFound) {
		t.Fatalf("取不存在链接的散列：%v, want ErrShareLinkNotFound", err)
	}
	if links, err := env.Store.ListShareLinks(ctx, env.Tenant, p.ID); err != nil || len(links) != 3 {
		t.Fatalf("链接列表：n=%d err=%v, want 3", len(links), err)
	}
}

func conformanceProjectOrganization(t *testing.T, ctx context.Context, env conformanceEnv) {
	p := mustCreateProject(t, env, ctx, "归集")
	tag, err := env.Store.CreateTag(ctx, env.Tenant, "归档标签", "")
	if err != nil {
		t.Fatalf("建标签：%v", err)
	}
	folder, err := env.Store.CreateFolder(ctx, env.Tenant, "归档分组", env.Owner, "")
	if err != nil {
		t.Fatalf("建分组：%v", err)
	}
	if err := env.Store.AttachTag(ctx, env.Tenant, p.ID, tag.ID); err != nil {
		t.Fatalf("贴标签：%v", err)
	}
	if err := env.Store.MoveProject(ctx, env.Tenant, p.ID, folder.ID); err != nil {
		t.Fatalf("移动项目：%v", err)
	}

	// 一次调用拿到可见项目的 folder 与 tags：前端列表就是靠它合并来源。
	org := mustOrg(t, env, ctx, p.ID)
	if org.FolderID != folder.ID || len(org.TagIDs) != 1 || org.TagIDs[0] != tag.ID {
		t.Fatalf("组织视图：%+v", org)
	}
	// 非成员看不到任何项目的组织信息（否则列表页会暴露别人的项目 ID）。
	other, err := env.Store.ListProjectOrganization(ctx, env.Tenant, env.Other)
	if err != nil || len(other) != 0 {
		t.Fatalf("非成员的组织视图：%+v err=%v", other, err)
	}
}

func mustOrg(t *testing.T, env conformanceEnv, ctx context.Context, projectID string) *ProjectOrg {
	t.Helper()
	orgs, err := env.Store.ListProjectOrganization(ctx, env.Tenant, env.Owner)
	if err != nil {
		t.Fatalf("ListProjectOrganization: %v", err)
	}
	for _, o := range orgs {
		if o.ProjectID == projectID {
			return o
		}
	}
	t.Fatalf("组织视图里没有项目 %s：%+v", projectID, orgs)
	return nil
}

// ─── SQLite 侧运行入口 ──────────────────────────────────────────────────────

func TestSQLiteConformance(t *testing.T) {
	runProjectStoreConformance(t, func(t *testing.T) conformanceEnv {
		store := newTestStore(t)
		return conformanceEnv{
			Store: store, Tenant: testTenant, Owner: testOwner, Other: testOther,
			OtherTenant: "tenant-b",
			NewUser: func(t *testing.T) string {
				t.Helper()
				return "guest-" + uuid.NewString()
			},
		}
	})
}
