// 任务详情里的一步要能看出是哪一页：页码、PPT 标题、概述。
// 页码从 1 起，与编辑器左侧一致。没有这些字段时调用方只显示步骤类型。

export type StepSubject = {
  page?: number;
  title?: string;
  summary?: string;
  segment?: number;
};

type Translate = (key: string, vars?: Record<string, string | number>) => string;

export function stepSubjectHeading(step: StepSubject, t: Translate): string {
  const page = step.page && step.page > 0 ? step.page : 0;
  const segment = step.segment && step.segment > 0 ? step.segment : 0;
  const title = step.title?.trim() ?? '';
  if (page && title && segment) return t('jobs.stepPageTitleSegment', { page, title, segment });
  if (page && title) return t('jobs.stepPageTitle', { page, title });
  if (page && segment) return t('jobs.stepPageSegment', { page, segment });
  if (page) return t('jobs.stepPage', { page });
  return title;
}

export type SlideLabel = {
  slideId: string;
  page?: number;
  title?: string;
  summary?: string;
};

export function affectedPageText(ids: string[], labels: SlideLabel[] | undefined, t: Translate): string {
  const byID = new Map((labels ?? []).map((label) => [label.slideId, label]));
  return ids
    .map((id) => {
      const label = byID.get(id);
      const heading = label ? stepSubjectHeading({ page: label.page, title: label.title }, t) : '';
      return heading || id;
    })
    .join('、');
}
