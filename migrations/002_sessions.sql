CREATE TABLE IF NOT EXISTS sessions (
    uuid UUID PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    subtitle TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    unified_mode TEXT NOT NULL DEFAULT '',
    workspace_id TEXT NOT NULL DEFAULT '',
    workspace_path TEXT NOT NULL DEFAULT '',
    context_usage_percent DOUBLE PRECISION,
    input_tokens BIGINT,
    output_tokens BIGINT,
    cache_read_tokens BIGINT,
    cache_write_tokens BIGINT,
    model_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    total_lines_added INT NOT NULL DEFAULT 0,
    total_lines_removed INT NOT NULL DEFAULT 0,
    files_changed_count INT NOT NULL DEFAULT 0,
    created_at_ms BIGINT,
    last_updated_at_ms BIGINT,
    extracted_at TIMESTAMPTZ,
    raw_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions (updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_sessions_workspace ON sessions (workspace_id);
CREATE INDEX IF NOT EXISTS idx_sessions_name ON sessions (name);

CREATE TABLE IF NOT EXISTS turns (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_uuid UUID NOT NULL REFERENCES sessions (uuid) ON DELETE CASCADE,
    bubble_id TEXT NOT NULL,
    ordinal INT NOT NULL,
    turn_type TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL DEFAULT '',
    tool_name TEXT NOT NULL DEFAULT '',
    mcp_name TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    duration_ms BIGINT,
    has_thinking BOOLEAN NOT NULL DEFAULT FALSE,
    thinking_duration_ms BIGINT,
    thinking_text TEXT NOT NULL DEFAULT '',
    input_tokens BIGINT,
    output_tokens BIGINT,
    cache_read_tokens BIGINT,
    cache_write_tokens BIGINT,
    bubble_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (session_uuid, bubble_id)
);

CREATE INDEX IF NOT EXISTS idx_turns_session_ordinal ON turns (session_uuid, ordinal);
CREATE INDEX IF NOT EXISTS idx_turns_thinking ON turns (session_uuid) WHERE has_thinking = TRUE;
