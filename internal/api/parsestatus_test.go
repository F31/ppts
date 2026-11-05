package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/pipeline"
	"github.com/F31/ppts/internal/project"
)

// parse-status 是编辑器的「能否调用 GetSlides」闸门：ready 必须与 GetSlides 的
// docKey 判定一致，否则前端又会在未解析时打出并无故障的 404。这里锁住各分支，
// 尤其是「排队任务从未被 worker 领取」必须如实报 stalled，而不是无限显示解析中。
func TestProjectParseStatusStates(t *testing.T) {
	const projectID = "project-1"
	docKey := objectstore.ObjectKey{
		TenantID: "tenant-1", ProjectID: projectID,
		Revision: "src-02", AssetType: "document", AssetID: "extracted", Ext: "json",
	}
	parseJob := func(state pipeline.JobState) []*pipeline.Job {
		return []*pipeline.Job{{
			ID: "job-parse", Kind: pipeline.KindParse, State: state,
			CreatedAt: time.Unix(100, 0), UpdatedAt: time.Unix(101, 0),
		}}
	}
	cases := []struct {
		name    string
		withDoc bool
		jobs    []*pipeline.Job
		want    string
	}{
		{"产物就绪优先于任务状态", true, parseJob(pipeline.StateFailed), "ready"},
		{"无产物且无解析任务", false, nil, "none"},
		{"无产物且仅有非 parse 任务", false, []*pipeline.Job{{
			ID: "job-narr", Kind: pipeline.KindNarration, State: pipeline.StateRunning, CreatedAt: time.Unix(100, 0),
		}}, "none"},
		{"无产物且解析进行中", false, parseJob(pipeline.StateRunning), "running"},
		{"无产物且解析失败", false, parseJob(pipeline.StateFailed), "failed"},
		{"刚排队仍算进行中", false, []*pipeline.Job{{
			ID: "job-fresh", Kind: pipeline.KindParse, State: pipeline.StateQueued,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}}, "running"},
		{"排队任务长时间无人领取", false, []*pipeline.Job{{
			ID: "job-stalled", Kind: pipeline.KindParse, State: pipeline.StateQueued,
			CreatedAt: time.Now().Add(-10 * time.Minute), UpdatedAt: time.Now().Add(-10 * time.Minute),
		}}, "stalled"},
		{"已领取的陈旧任务不算卡住", false, []*pipeline.Job{{
			ID: "job-claimed", Kind: pipeline.KindParse, State: pipeline.StateRunning,
			Attempt: 1, LeaseOwner: "worker-1",
			CreatedAt: time.Now().Add(-10 * time.Minute), UpdatedAt: time.Now().Add(-10 * time.Minute),
		}}, "running"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projects := &fakeProjectStore{projects: []*project.Project{{
				ID: projectID, TenantID: "tenant-1", OwnerUser: "user-1", Title: "解析状态", CurrentRevision: 2,
			}}}
			objects := testObjects(t)
			if tc.withDoc {
				putAPIObject(t, objects, docKey, []byte(`{"schemaVersion":"1.0","pages":[]}`), "application/json")
			}
			jobs := &jobCreatorStub{listJobs: tc.jobs}
			server := httptest.NewServer(NewHandler(projects, newFakeUploadStore(), &fakeScriptStore{}, jobs, &fakeArtifactStore{}, objects, nil, Options{DevHeaders: true}))
			t.Cleanup(server.Close)

			req, err := http.NewRequest(http.MethodGet, server.URL+"/projects/"+projectID+"/parse-status", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set(tenantHeader, "tenant-1")
			req.Header.Set(userHeader, "user-1")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			var body struct {
				Status  string `json:"status"`
				Message string `json:"message"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Status != tc.want {
				t.Fatalf("status = %q, want %q (message=%q)", body.Status, tc.want, body.Message)
			}
		})
	}
}

func TestProjectParseStatusRequiresIdentity(t *testing.T) {
	server := httptest.NewServer(NewHandler(
		&fakeProjectStore{}, newFakeUploadStore(), &fakeScriptStore{}, &jobCreatorStub{},
		&fakeArtifactStore{}, testObjects(t), nil, Options{DevHeaders: true},
	))
	t.Cleanup(server.Close)
	resp, err := http.Get(server.URL + "/projects/project-1/parse-status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}
