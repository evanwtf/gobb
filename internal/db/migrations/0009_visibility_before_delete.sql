-- A delete keeps the state of the row, so that a restore puts it back.
--
-- Posts and topics have three states: approved, unapproved (held) and deleted.
-- Without a record of the state before a delete, a restore cannot tell a held
-- post from an approved one, and a restore would approve what a moderator never
-- approved. The column holds the state that the row had when it was deleted, and
-- it is null for a row that is not deleted. A row that was deleted before this
-- migration, or imported as deleted, has no state to put back: the restore
-- decides (see board.RestorePost).

alter table posts add column visibility_before_delete text null
	check (visibility_before_delete in ('approved', 'unapproved')),
	add constraint posts_before_delete_only_when_deleted
	check (visibility_before_delete is null or visibility = 'deleted');

alter table topics add column visibility_before_delete text null
	check (visibility_before_delete in ('approved', 'unapproved')),
	add constraint topics_before_delete_only_when_deleted
	check (visibility_before_delete is null or visibility = 'deleted');

-- The unlock rule of topic.lock_own reads the latest topic.lock row of a topic.
create index mod_log_target_idx on mod_log (target_type, target_id, id);
