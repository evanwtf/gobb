-- The topic page reads the attachments of a page of posts by post_id.
create index attachments_post_idx on attachments (post_id);
-- The forum page pins every global topic of the forums that the viewer may read,
-- and the announcements of the forum, newest activity first. Few topics are of
-- these kinds, so each partial index holds only those.
create index topics_global_idx on topics (last_post_at desc, id desc) where kind = 'global';
create index topics_announce_idx on topics (forum_id, last_post_at desc, id desc) where kind = 'announce';
