-- The event log: every accepted batch, in board order. Rows move into
-- compressed segments in W5 (see docs/decisions/0004-storage.md).
CREATE TABLE ops (
    board_id    text        NOT NULL REFERENCES boards (id),
    seq         bigint      NOT NULL,
    client_id   bigint      NOT NULL,
    client_seq  bigint      NOT NULL,
    -- protobuf StoredBatch: stamp + ops
    batch       bytea       NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    -- Also the fence against two writers for one board (ADR-0005).
    PRIMARY KEY (board_id, seq)
);

-- Keyframes for loading boards and, later, for history replay.
CREATE TABLE snapshots (
    board_id    text        NOT NULL REFERENCES boards (id),
    seq         bigint      NOT NULL,
    -- zstd(protobuf BoardSnapshot)
    data        bytea       NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (board_id, seq)
);
