-- Internal sync cache (2026-11, ara integration).
--
-- sync_meta holds the parsed header facts the internal sync API returns
-- (Message-ID, In-Reply-To, References, From/To/Cc, Date, attachment
-- presence). Lazy like preview/to_addr: NULL = not parsed yet; the first sync
-- that reaches the row parses the blob once and stores the result, so the
-- delivery path stays free of MIME parsing cost and old mail is covered too.
ALTER TABLE message ADD COLUMN IF NOT EXISTS sync_meta jsonb;
