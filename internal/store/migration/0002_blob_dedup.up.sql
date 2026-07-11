-- 0002 blob lifecycle: content-addressed dedup + orphan cleanup baseline.
--
-- message_blob.sha256 becomes the content address: appends reuse an existing
-- blob row instead of storing the same RFC822 bytes again (multi-recipient
-- local fan-out, Sent copies). Delete paths garbage-collect blobs that lost
-- their last reference (candidate-based, inline — no cron).

-- 1) dedupe existing rows so the unique index can build: repoint messages to
--    the oldest blob per sha256, then drop the now-orphaned duplicates.
UPDATE message m
SET blob_id = canonical.id
FROM message_blob b
JOIN LATERAL (
    SELECT id FROM message_blob c
    WHERE c.sha256 = b.sha256
    ORDER BY created_at, id
    LIMIT 1
) canonical ON true
WHERE m.blob_id = b.id AND b.id <> canonical.id;

DELETE FROM message_blob b
WHERE NOT EXISTS (SELECT 1 FROM message m WHERE m.blob_id = b.id);

-- 2) content address is unique from here on.
DROP INDEX idx_message_blob_sha256;
CREATE UNIQUE INDEX idx_message_blob_sha256 ON message_blob (sha256);

-- 3) GC needs the reverse lookup (blob → referencing messages).
CREATE INDEX idx_message_blob_ref ON message (blob_id);
