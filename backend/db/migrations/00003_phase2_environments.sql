-- +goose Up
CREATE TABLE environments (
    bot_id UUID PRIMARY KEY REFERENCES bots(id) ON DELETE CASCADE,
    driver VARCHAR(20) NOT NULL CHECK (driver IN ('docker', 'kubernetes', 'fake')),
    env_id VARCHAR(255) NOT NULL DEFAULT '',
    status VARCHAR(20) NOT NULL DEFAULT 'stopped' CHECK (status IN ('creating', 'running', 'stopped', 'error')),
    image VARCHAR(255) NOT NULL DEFAULT 'botbox/sandbox:dev',
    cpu_millis INT NOT NULL DEFAULT 2000 CHECK (cpu_millis > 0),
    memory_mib INT NOT NULL DEFAULT 2048 CHECK (memory_mib > 0),
    volume_ref VARCHAR(255) NOT NULL DEFAULT '',
    control_holder_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    control_note TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_environments_status ON environments (status);

-- +goose Down
DROP INDEX IF EXISTS idx_environments_status;
DROP TABLE IF EXISTS environments;
