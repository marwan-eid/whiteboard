-- Board owners and share links (W6). Owners are guest ids from signed guest
-- tokens; boards created by visiting an unknown id stay public and ownerless.
ALTER TABLE boards ADD COLUMN owner_id text;
CREATE INDEX boards_owner ON boards (owner_id, created_at DESC) WHERE owner_id IS NOT NULL;

CREATE TABLE share_links (
    id          text        PRIMARY KEY,
    board_id    text        NOT NULL REFERENCES boards (id),
    -- sha256 of the token; the token itself is only ever shown to the owner once.
    token_hash  bytea       NOT NULL UNIQUE,
    role        text        NOT NULL CHECK (role IN ('viewer', 'editor')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    revoked_at  timestamptz
);
CREATE INDEX share_links_board ON share_links (board_id);
