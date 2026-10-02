-- Usage counts for the public demo (docs/BENCHMARKS.md target 7): which guests
-- edited on a day (only a hash of the random guest id), and which boards had
-- two or more people connected at once.
CREATE TABLE usage_guests (
    day         date  NOT NULL,
    guest_hash  bytea NOT NULL,
    PRIMARY KEY (day, guest_hash)
);
CREATE TABLE usage_boards (
    day       date NOT NULL,
    board_id  text NOT NULL,
    PRIMARY KEY (day, board_id)
);
