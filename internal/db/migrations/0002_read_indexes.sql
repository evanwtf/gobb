-- The read side lists the topics of a forum by newest activity, and finds the
-- posts of a member.
create index topics_forum_last_post_idx on topics (forum_id, last_post_at desc, id desc);
create index posts_author_idx on posts (author_id);
