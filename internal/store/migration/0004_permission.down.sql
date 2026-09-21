DROP TABLE IF EXISTS send_counter;
ALTER TABLE domain
    DROP COLUMN IF EXISTS allow_receive_external,
    DROP COLUMN IF EXISTS allow_send_external;
ALTER TABLE account
    DROP COLUMN IF EXISTS daily_send_limit,
    DROP COLUMN IF EXISTS can_receive_external,
    DROP COLUMN IF EXISTS can_send_external,
    DROP COLUMN IF EXISTS can_send;
