-- The password epoch counts the changes of a member's password.
--
-- A sign-in proves a password, then starts a session. A password change or an
-- administrator's reset in between must leave the member with no session from
-- that sign-in. The check compares the epoch that the sign-in read with the one
-- that the row holds: every change of the password adds one. A rehash (the same
-- password, stored in the gobb form) leaves the epoch alone, so two sign-ins at
-- once on a legacy hash both succeed.

alter table users add column password_epoch bigint not null default 0;
