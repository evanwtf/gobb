-- Search. The posts already carry a generated tsvector with a GIN index.

-- A search also matches the title of a topic, against its first post. This
-- index serves that match. The expression is the one the query writes.
create index topics_title_search_idx on topics
	using gin (to_tsvector('english'::regconfig, title));

-- A search by author lists that member's approved posts, newest first. Search
-- shows approved posts only, so the index holds only those.
create index posts_author_created_idx on posts (author_id, created_at desc, id desc)
	where visibility = 'approved';
