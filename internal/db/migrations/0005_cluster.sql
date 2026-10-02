-- Node membership and board leases (W8, ADR-0005). Times come from the
-- database's now(), so node clocks do not need to agree.
CREATE TABLE nodes (
    id            text        PRIMARY KEY,
    addr          text        NOT NULL DEFAULT '',
    heartbeat_at  timestamptz NOT NULL
);

-- The node serving a board. epoch increases with every acquisition, so a
-- node whose lease was taken over cannot renew it.
CREATE TABLE board_leases (
    board_id    text        PRIMARY KEY,
    node_id     text        NOT NULL,
    epoch       bigint      NOT NULL,
    expires_at  timestamptz NOT NULL
);
CREATE INDEX board_leases_node ON board_leases (node_id);
