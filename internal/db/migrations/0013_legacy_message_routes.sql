-- Bundle format 2 adds the route kinds 'messages' and 'message' for the old
-- links to private messages. The check on legacy_routes.target_kind lists the
-- kinds, so it is replaced with a list that holds both. Migration 0001 is not
-- edited: a database that has applied it keeps its record.
alter table legacy_routes drop constraint legacy_routes_target_kind_check;
alter table legacy_routes add constraint legacy_routes_target_kind_check
	check (target_kind in (
		'home', 'forum', 'topic', 'post', 'user', 'members', 'team',
		'attachment', 'avatar', 'search', 'account', 'page',
		'messages', 'message'));
