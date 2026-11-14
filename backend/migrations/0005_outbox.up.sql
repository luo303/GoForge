-- 0005：按事件型 outbox 重建消息表（不迁就旧的 jobs 表）。
--
-- 与用户给的模板一致的部分：message_id 用于消费端去重，topic 用于路由，
-- msg_key 用于保序（同一业务对象串行），payload 自包含，status 标记发没发。
--
-- 额外三个字段是"可靠投递"必需、模板里省略的：
--   attempts    —— 投递失败重试次数，用来算退避与判死
--   available_at—— 退避到期时间，relay 只捞到点的
--   last_error  —— 最后一次投递失败的原因，排查时不必翻日志
CREATE TABLE IF NOT EXISTS outbox (
    id           BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    message_id   UUID        NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    topic        VARCHAR(128) NOT NULL,
    msg_key      VARCHAR(64) NOT NULL,
    payload      JSONB       NOT NULL,
    status       SMALLINT    NOT NULL DEFAULT 0,
    attempts     INT         NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error   TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    CONSTRAINT outbox_status_check CHECK (status IN (0, 1, 2))
);

-- relay 只捞"待发送且到点"的行，按 id 保证 FIFO。
-- 部分索引排除已发送的行，索引体积只跟积压量有关，不跟历史量有关。
CREATE INDEX IF NOT EXISTS idx_outbox_pending
    ON outbox (available_at, id) WHERE status = 0;

-- 页面要查"这条业务对象的最近一条消息"（进度、失败原因），按 msg_key 倒序最常用。
CREATE INDEX IF NOT EXISTS idx_outbox_msg_key
    ON outbox (msg_key, created_at DESC);

-- 消费端去重：同一 message_id 只处理一次。
-- 独立成表而不是靠业务表状态，因为将来会有非文档主题的消息。
CREATE TABLE IF NOT EXISTS consumed_messages (
    message_id  UUID        PRIMARY KEY,
    topic       VARCHAR(128) NOT NULL,
    consumed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
