-- 0002：Knowledge 模块的文档与分块表。
--
-- 表形状按 docs/design/overview.md 5.1 节落地，一次建全而不是每个阶段加列：
-- 这些列属于"文档这条记录本来就该有什么"，分阶段加列会让中间版本的表语义变得
-- 难以解释（例如 Phase 5 之前 collection 到底表示"未分类"还是"不支持分类"）。
-- 注释里标明哪一阶段开始写入，Phase 4 只写其中一部分，其余保持 NULL / 默认值。

CREATE TABLE documents (
    id            bigserial PRIMARY KEY,
    title         text        NOT NULL,
    -- file：上传文件；note：页面新建的笔记；web：Phase 5 的网页抓取
    source_type   text        NOT NULL,
    source_url    text,                              -- Phase 5 起写入
    mime          text        NOT NULL,
    size_bytes    bigint      NOT NULL DEFAULT 0,
    -- SHA-256（十六进制小写）。唯一索引是"重复导入"的判定依据：
    -- 幂等由数据库兜底，而不是靠"先查再插"这种并发下有竞态的写法。
    content_hash  char(64)    NOT NULL,
    storage_path  text,                              -- 原始文件相对 STORAGE_DIR 的路径；note 为空
    content       text,                              -- note 正文 / Phase 5 网页正文；file 为空
    collection    text,                              -- Phase 5 起写入
    tags          text[]      NOT NULL DEFAULT '{}',  -- Phase 5 起写入
    status        text        NOT NULL DEFAULT 'pending',
    chunk_count   integer     NOT NULL DEFAULT 0,
    error_code    text,                              -- Phase 6 起写入（异步任务失败）
    error_message text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT documents_source_type_check
        CHECK (source_type IN ('file', 'note', 'web')),
    -- 状态机：pending → parsing → indexing → ready；失败落 failed，取消落 cancelling。
    -- 用 CHECK 而不是 PG 枚举类型：加一个状态只需要改约束，不需要 ALTER TYPE。
    CONSTRAINT documents_status_check
        CHECK (status IN ('pending', 'parsing', 'indexing', 'ready', 'failed', 'cancelling')),
    CONSTRAINT documents_size_bytes_check CHECK (size_bytes >= 0),
    CONSTRAINT documents_chunk_count_check CHECK (chunk_count >= 0)
);

CREATE UNIQUE INDEX documents_content_hash_key ON documents (content_hash);
-- 列表默认按最近创建倒序翻页，没有这个索引会退化成全表排序。
CREATE INDEX documents_created_at_idx ON documents (created_at DESC, id DESC);
CREATE INDEX documents_tags_idx ON documents USING gin (tags);

CREATE TABLE document_chunks (
    id           bigserial PRIMARY KEY,
    document_id  bigint  NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    -- 文档内序号，从 0 开始。(document_id, seq) 唯一 → 重跑同一份内容时
    -- 可以先删后插，不会产生重复分块。
    seq          integer NOT NULL,
    content      text    NOT NULL,
    heading      text,                              -- 所在章节标题；文首无标题时为 NULL
    -- 原始正文的偏移，单位是 Unicode 码点（rune），不是字节。
    -- 选码点是因为它对中文与 emoji 都按"一个字符"计数，与前端 Array.from 的口径一致；
    -- 字节偏移会让"第 100 个字"在中文文档里变成第 300 个字节，人无法核对。
    start_offset integer NOT NULL,
    end_offset   integer NOT NULL,
    token_count  integer NOT NULL DEFAULT 0,
    embedding    vector(1536),                      -- Phase 7 起写入
    -- 生成列：写 content 时数据库自动维护，业务代码不需要也不能写它。
    -- 'simple' 只是占位配置（不做词干化、不分中文词），Phase 8 做混合检索时
    -- 会重新评估要不要换 zhparser/pg_bigm，那时改表达式需要 drop/add 列。
    tsv          tsvector GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED,
    created_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT document_chunks_seq_check CHECK (seq >= 0),
    CONSTRAINT document_chunks_offsets_check
        CHECK (start_offset >= 0 AND end_offset >= start_offset),
    CONSTRAINT document_chunks_document_seq_key UNIQUE (document_id, seq)
);

CREATE INDEX document_chunks_tsv_idx ON document_chunks USING gin (tsv);
