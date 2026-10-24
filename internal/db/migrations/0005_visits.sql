-- A member's last request of the visit in progress. A visit ends after 30 idle
-- minutes; the next request moves last_visit_at to this time.
alter table users add column last_active_at timestamptz null;

-- "New since your last visit", the unread flags of the index, and the unread
-- check of the forum page all ask for the topics whose last post is newer than a
-- time, newest first. This index reads only those topics, so a request does not
-- scan the whole table.
create index topics_last_post_idx on topics (last_post_at desc, id desc);
