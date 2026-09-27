-- Boards. Ids are short URL-safe strings (see protocol.ValidBoardID) so that
-- well-known boards such as "demo" can exist.
CREATE TABLE boards (
    id          text PRIMARY KEY,
    title       text NOT NULL DEFAULT '',
    visibility  text NOT NULL DEFAULT 'public' CHECK (visibility IN ('public', 'private')),
    created_at  timestamptz NOT NULL DEFAULT now()
);
