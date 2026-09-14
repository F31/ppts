-- ppts 用量账本成本分账列（0020，2026-09-13；G3-2 定价表/成本分账）
-- 目标：usage_ledger 记录"供应商成本 vs 用户计费"分离金额（V4.0 §12.2），
-- 由内部定价表（internal/pricing，PPTS_PRICE_BOOK 覆盖）在结算时计算并落账；
-- currency 记录币种，price_version 记录定价版本，便于追溯。

ALTER TABLE usage_ledger
    ADD COLUMN IF NOT EXISTS user_amount  numeric NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS supplier_cost numeric NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS currency     text NOT NULL DEFAULT '';
