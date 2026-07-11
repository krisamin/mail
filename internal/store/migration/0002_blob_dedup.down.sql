-- 0002 down: relax the content address back to a plain lookup index.
DROP INDEX IF EXISTS idx_message_blob_ref;
DROP INDEX IF EXISTS idx_message_blob_sha256;
CREATE INDEX idx_message_blob_sha256 ON message_blob (sha256);
