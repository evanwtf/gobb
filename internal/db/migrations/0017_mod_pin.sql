-- The moderator option mod.pin sets the type of a topic: normal, sticky or an
-- announcement. The members who may lock topics today may also set the type:
-- every mod.lock setting gets a mod.pin setting with the same holder, scope and
-- value. A Never stays a Never. The option names live in acl.Options; this file
-- only copies rows.
--
-- A role holds an option once (the primary key), so a role that holds mod.pin
-- already keeps its own setting. A grant row has no such key, so the copy skips a
-- holder that already has a mod.pin row in that scope.

insert into acl_role_settings (role_id, option, setting)
select role_id, 'mod.pin', setting
  from acl_role_settings
 where option = 'mod.lock'
on conflict (role_id, option) do nothing;

insert into acl_group_grants (group_id, forum_id, option, setting)
select g.group_id, g.forum_id, 'mod.pin', g.setting
  from acl_group_grants g
 where g.option = 'mod.lock'
   and not exists (select 1 from acl_group_grants p
                    where p.group_id = g.group_id and p.forum_id = g.forum_id and p.option = 'mod.pin');

insert into acl_user_grants (user_id, forum_id, option, setting)
select u.user_id, u.forum_id, 'mod.pin', u.setting
  from acl_user_grants u
 where u.option = 'mod.lock'
   and not exists (select 1 from acl_user_grants p
                    where p.user_id = u.user_id and p.forum_id = u.forum_id and p.option = 'mod.pin');
