-- Defensive Mode asks whether a client range is a trusted address of any member
-- in good standing: user_ips rows with ip <<= the client's range, for every
-- member. The primary key (user_id, ip) does not serve a query on ip alone, so
-- this index does. A GiST index with inet_ops serves the <<= operator.
create index user_ips_ip_idx on user_ips using gist (ip inet_ops);
