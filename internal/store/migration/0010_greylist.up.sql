-- 0010: greylisting triplets (source network, envelope from, envelope rcpt).
-- First contact gets 451; a retry after the delay passes and the triplet is
-- then trusted (last_seen keeps refreshing). Stale rows are pruned inline.
CREATE TABLE greylist (
    source_net    TEXT NOT NULL,  -- /24 (IPv4) or /64 (IPv6) of the client
    envelope_from TEXT NOT NULL,  -- lowercased ('' = bounce sender <>)
    envelope_rcpt TEXT NOT NULL,  -- lowercased
    first_seen    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen     TIMESTAMPTZ NOT NULL DEFAULT now(),
    pass_count    BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (source_net, envelope_from, envelope_rcpt)
);

CREATE INDEX greylist_last_seen_idx ON greylist (last_seen);
