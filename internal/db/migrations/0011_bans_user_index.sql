-- Every request that carries a session asks whether a user ban holds the member
-- (session.Lookup). The lookup reads this index, so the cost of a request does
-- not grow with the number of bans.

create index bans_user_idx on bans (user_id) where kind = 'user';
