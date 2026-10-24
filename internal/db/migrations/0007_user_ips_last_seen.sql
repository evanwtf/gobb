-- The hourly cleanup deletes the user_ips rows last seen more than 90 days ago.
-- Without this index the delete reads the whole table every hour.
create index user_ips_last_seen_idx on user_ips (last_seen_at);
