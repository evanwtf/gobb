-- Sign-out and the account pages delete every session of a user, and the cleanup
-- of ended sessions finds them by expiry.
create index sessions_user_idx on sessions (user_id);
create index sessions_expires_idx on sessions (expires_at);
