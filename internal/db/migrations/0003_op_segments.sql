-- Compacted log: runs of old ops, zstd(protobuf LogSegment). Rows move here
-- from ops in the same transaction that deletes them, so a seq is always in
-- exactly one place.
CREATE TABLE op_segments (
    board_id    text        NOT NULL REFERENCES boards (id),
    from_seq    bigint      NOT NULL,
    to_seq      bigint      NOT NULL,
    data        bytea       NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (board_id, from_seq)
);
