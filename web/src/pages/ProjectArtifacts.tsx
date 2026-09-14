import { useCallback, useEffect, useState } from 'react';
import { getNarration, getProjectSlides, type ClientIdentity } from '../api';
import { Link } from '../router';

type State = {
  loading: boolean;
  slideCount: number;
  ready: boolean;
  timelineKey: string;
  pagePngCount: number;
  revisionNo: number;
  unavailReason?: string;
};

export function ProjectArtifacts({ identity, projectId }: { identity: ClientIdentity; projectId: string }) {
  const [state, setState] = useState<State>({ loading: true, slideCount: 0, ready: false, timelineKey: '', pagePngCount: 0, revisionNo: 0 });

  const load = useCallback(async () => {
    setState((current) => ({ ...current, loading: true }));
    try {
      let slideCount = 0;
      try {
        const slides = await getProjectSlides(identity, projectId);
        slideCount = slides.slides.length;
      } catch {
        // 解析未完成或不可用。
      }
      const narration = await getNarration(identity, projectId);
      setState({
        loading: false,
        slideCount,
        ready: narration.ready,
        timelineKey: narration.timelineKey,
        pagePngCount: narration.pagePngKeys?.length ?? 0,
        revisionNo: narration.revisionNo ?? 0
      });
    } catch (err) {
      setState((current) => ({ ...current, loading: false, unavailReason: err instanceof Error ? err.message : '状态查询失败' }));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [identity, projectId]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div className="page-stack">
      <section className="page-header-row">
        <div>
          <span className="eyebrow">项目成品与版本</span>
          <h1 title={projectId}>{projectId.slice(0, 14)}</h1>
          <small className="page-sub">成品绑定固定生成快照；当前草稿更新会在播放器提示「此前版本」。</small>
        </div>
        <div className="page-actions">
          <Link to={`/projects/${projectId}/editor`} className="button-primary">
            返回工作台
          </Link>
          <Link to="/jobs" className="button-ghost">
            任务中心
          </Link>
        </div>
      </section>

      {state.loading ? (
        <p className="empty-state">加载中…</p>
      ) : (
        <>
          <section className="panel">
            <span className="eyebrow">当前配音快照</span>
            {state.ready ? (
              <dl className="detail-grid">
                <div>
                  <dt>状态</dt>
                  <dd>
                    <span className="state-tag succeeded">已配音</span>
                  </dd>
                </div>
                <div>
                  <dt>页面数</dt>
                  <dd>{state.slideCount}</dd>
                </div>
                <div>
                  <dt>页面渲染图</dt>
                  <dd>{state.pagePngCount > 0 ? `${state.pagePngCount} 张` : '未渲染（音频+字幕可播）'}</dd>
                </div>
                <div>
                  <dt>对应源版本</dt>
                  <dd>rev {state.revisionNo || '（未知）'}</dd>
                </div>
                <div>
                  <dt>时间轴</dt>
                  <dd className="nowrap-ellipsis">{state.timelineKey}</dd>
                </div>
              </dl>
            ) : (
              <div className="empty-state first-run">
                <p>
                  {state.unavailReason
                    ? `配音状态查询不可用：${state.unavailReason}。`
                    : '该项目尚无成功配音。先在编辑器生成讲稿与配音，完成后这里会出现播放与导出入口。'}
                </p>
                <Link to={`/projects/${projectId}/editor`} className="button-primary">
                  去生成配音
                </Link>
              </div>
            )}
          </section>

          <section className="panel">
            <span className="eyebrow">导出成品</span>
            <p className="empty-state">
              导出为异步任务（Web 讲解工程 / MP4 / 音频包 / 字幕）。从工作台点击「导出为 Web 工程」创建任务后，
              在任务中心跟踪进度；本页面暂未聚合成品列表接口，不展示未验证的成品项。
            </p>
          </section>
        </>
      )}
    </div>
  );
}