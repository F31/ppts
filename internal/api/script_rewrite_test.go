package api

import "testing"

func TestCleanRewriteTextDropsMetaReasoning(t *testing.T) {
	input := "用户要求我润色一段关于AI架构的PPT演示讲解文案。让我分析原文，保留事实并调整表达。\n\n润色后：AI 架构的核心，是让感知、推理与执行形成稳定闭环。"
	got := cleanRewriteText(input)
	want := "AI 架构的核心，是让感知、推理与执行形成稳定闭环。"
	if got != want {
		t.Fatalf("cleanRewriteText() = %q, want %q", got, want)
	}
}

func TestCleanRewriteTextDropsSimplePrefix(t *testing.T) {
	got := cleanRewriteText("最终稿：这页重点介绍三层 AI 架构。")
	want := "这页重点介绍三层 AI 架构。"
	if got != want {
		t.Fatalf("cleanRewriteText() = %q, want %q", got, want)
	}
}

func TestCleanRewriteTextDropsUserHopeRequirementBlock(t *testing.T) {
	input := ";用户希望将一段PPT演讲稿进行润色，压缩为更短、更适合口播的版本。\n\n要求：\n- 保留关键事实\n- 不新增信息\n\n这页我们重点看 AI 架构如何把模型能力转化为稳定的业务流程。"
	got := cleanRewriteText(input)
	want := "这页我们重点看 AI 架构如何把模型能力转化为稳定的业务流程。"
	if got != want {
		t.Fatalf("cleanRewriteText() = %q, want %q", got, want)
	}
}

func TestCleanRewriteTextExtractsCandidateFromReasoning(t *testing.T) {
	input := `分析原文结构：
- 核心判断：迈向"机器智能"时代
- 大会全景 / 4（这个数字需要保留）

需要压缩为口播讲稿，减少字数，保留关键信息。

改写思路：
1. 核心判断改为更口语化的表达

尝试改写：

各位，本次大会释放出一个明确信号：我们正迈入"机器智能"时代。共有四点关键趋势。

检查是否保留了所有数字和单位、专有名词：
- "机器智能" ✓

再调整一下，确保口语化且更简短：

本届大会指向一个明确方向：我们正加速进入"机器智能"时代，主要呈现四大变化。首先是目标转移——Qwen 不再执着于榜单成绩，而是聚焦真实场景下的任务闭环。其次是价值升级——云服务的核心从提供算力转向运营智力，AI SaaS 可能转向按任务结果收费。最后是技术整合——芯片、模型、云与智能体实现全链路协同，全面支撑生产级 AI 落地。

检查"4"的处理：原文是"大会全景 4"。`
	got := cleanRewriteText(input)
	want := `本届大会指向一个明确方向：我们正加速进入"机器智能"时代，主要呈现四大变化。首先是目标转移——Qwen 不再执着于榜单成绩，而是聚焦真实场景下的任务闭环。其次是价值升级——云服务的核心从提供算力转向运营智力，AI SaaS 可能转向按任务结果收费。最后是技术整合——芯片、模型、云与智能体实现全链路协同，全面支撑生产级 AI 落地。`
	if got != want {
		t.Fatalf("cleanRewriteText() = %q, want %q", got, want)
	}
}
