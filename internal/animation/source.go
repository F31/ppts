package animation

// ReconcileTriggerSources 在保存时把「trigger 被改过」的预填来源降级为 manual。
//
// 为什么需要这一步：编辑器保存的是**整份规格**（PUT 全量替换），服务端看到的只是一个
// 新的 trigger 值，看不出它是不是人改的。而 Q7 决策要求「用户修改预填绑定的 trigger 后
// 来源 MUST NOT 保留原规则名」——留着会让编辑器把一条人工结论算进「规则 text 的 18 条」，
// 批量撤销时连它一起撤掉，用户刚做的修改无声消失。
//
// 判据只有一条：**同一个 clip 的 trigger 变了，且旧来源是规则名 → 置 manual**。
// 不比对 duration/params：来源描述的是「这条绑定从哪来」，不是「整条 clip 没动过」；
// 用户只调时长不该把绑定结论一起作废。
//
// 已知边界（写出来而不是假装没有）：
//   - 前端若在保存时给改过的 clip 换了新 id，这里匹配不上，来源会保持原值。
//     服务端无从判断「新 id 的这条 clip」和「旧 id 那条」是同一条——按位置猜会在
//     用户增删 clip 时错杀。这条边界由前端的编辑路径负责（见 tasks 4.3）。
//   - 旧 clip 没有来源标注（手写规格、旧客户端）时不动：本来就没有规则名可清。
//
// 返回被降级的条数，便于调用方打点与测试。
func ReconcileTriggerSources(prev, next *Revision) int {
	if prev == nil || next == nil {
		return 0
	}
	prevClips := make(map[string][]*Clip, len(prev.Tracks))
	for _, t := range prev.Tracks {
		if t == nil {
			continue
		}
		prevClips[t.ElementID] = append(prevClips[t.ElementID], t.Clips...)
	}

	changed := 0
	for _, t := range next.Tracks {
		if t == nil {
			continue
		}
		olds := prevClips[t.ElementID]
		if len(olds) == 0 {
			continue
		}
		for i, c := range t.Clips {
			if c == nil {
				continue
			}
			old := matchPrevClip(olds, c, i)
			if old == nil || c.Trigger == old.Trigger {
				continue
			}
			if old.TriggerSource == "" || old.TriggerSource == TriggerSourceManual {
				continue
			}
			c.TriggerSource = TriggerSourceManual
			changed++
		}
	}
	return changed
}

// matchPrevClip 按 clip id 匹配；无 id 时退回同一元素下的位置。
//
// 有 id 却匹配不上时返回 nil 而不是退回位置：用户在中间插入一条 clip 会让位置整体错位，
// 按位置猜就会把「A 的结论」安到「B」头上，比不匹配更糟。
func matchPrevClip(olds []*Clip, c *Clip, index int) *Clip {
	if c.ID != "" {
		for _, o := range olds {
			if o != nil && o.ID == c.ID {
				return o
			}
		}
		return nil
	}
	if index < len(olds) {
		return olds[index]
	}
	return nil
}
