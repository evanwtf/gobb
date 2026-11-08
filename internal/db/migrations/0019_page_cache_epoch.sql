-- The guest page cache of `gobb serve` lives in the memory of that process. A
-- command that writes the board from another process (gobb user, gobb ban,
-- gobb messages, gobb import) sets a new random value here, and the server
-- empties its cache when it sees the value change. One row at most.
create table page_cache_epoch (
	id    int    primary key check (id = 1),
	value bigint not null
);
