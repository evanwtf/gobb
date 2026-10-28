-- What one Spammer action changed, so that its Undo can put it back. Each row
-- belongs to the mod_log row of the action. kind names the row:
--
--   post          posts.id = row_id; the action deleted it.
--   topic         topics.id = row_id; the action deleted it.
--   pm_recipient  pm_recipients (row_id, user_id); the action set is_deleted.
--   user_ip       user_ips (user_id, ip, seen_at); the ban removed it.
--
-- The bans of the action carry its log ID in bans.undo_log_id.
--
-- No foreign key to the rows: the Undo joins to them, so a row that is gone is
-- skipped.
create table mod_undo (
	log_id  bigint not null
		references mod_log (id) on delete cascade deferrable initially immediate,
	kind    text   not null check (kind in ('post', 'topic', 'pm_recipient', 'user_ip')),
	row_id  bigint null,
	user_id bigint null,
	ip      inet   null,
	seen_at timestamptz null
);
create index mod_undo_log on mod_undo (log_id, kind);
