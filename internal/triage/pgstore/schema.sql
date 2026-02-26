-- Triage results track the lifecycle of each alert investigation.
-- The partial unique index on fingerprint prevents concurrent triage of the same alert.
CREATE TABLE IF NOT EXISTS triage_runs (
    id           TEXT PRIMARY KEY,
    fingerprint  TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending',
    alert_name   TEXT NOT NULL DEFAULT '',
    severity     TEXT NOT NULL DEFAULT '',
    summary      TEXT NOT NULL DEFAULT '',
    analysis     TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    duration_s     DOUBLE PRECISION NOT NULL DEFAULT 0,
    tokens_used  INTEGER NOT NULL DEFAULT 0,
    tool_calls   INTEGER NOT NULL DEFAULT 0,
    tools_used   JSONB NOT NULL DEFAULT '[]',
    system_prompt TEXT NOT NULL DEFAULT '',
    model         TEXT NOT NULL DEFAULT '');

CREATE INDEX IF NOT EXISTS idx_triage_runs_fingerprint ON triage_runs (fingerprint);
CREATE INDEX IF NOT EXISTS idx_triage_runs_status ON triage_runs (status);

-- Partial index to enforce uniqueness of active triage results by fingerprint, allowing multiple completed triages for the same alert.
CREATE UNIQUE INDEX IF NOT EXISTS idx_triage_runs_active_fingerprint
ON triage_runs(fingerprint)
WHERE status IN ('pending', 'in_progress');

-- Messages capture the full LLM conversation for replay and analysis.
-- Each message is one turn in the user/assistant exchange.
CREATE TABLE IF NOT EXISTS messages (
    id         SERIAL PRIMARY KEY,
    triage_id  TEXT NOT NULL REFERENCES triage_runs(id),
    seq        INTEGER NOT NULL,
    role       TEXT NOT NULL,
    content    JSONB NOT NULL,
    tokens_in  INTEGER,
    tokens_out INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    duration_s   DOUBLE PRECISION,
    stop_reason  TEXT,
    model        TEXT
);

-- Tool calls log each invocation of an external tool during the triage process, including inputs, outputs, and errors.
CREATE TABLE IF NOT EXISTS tool_calls (
    id           SERIAL PRIMARY KEY,
    triage_id    TEXT NOT NULL REFERENCES triage_runs(id),
    message_id   INTEGER NOT NULL REFERENCES messages(id),
    message_seq  INTEGER NOT NULL,
    tool_name    TEXT NOT NULL,
    input        JSONB NOT NULL,
    output       JSONB,
    input_bytes  INTEGER NOT NULL DEFAULT 0,
    output_bytes INTEGER NOT NULL DEFAULT 0,
    is_error     BOOLEAN NOT NULL DEFAULT false,
    duration_s   DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_messages_triage_id ON messages(triage_id);
CREATE INDEX IF NOT EXISTS idx_tool_calls_triage_id ON tool_calls(triage_id);
