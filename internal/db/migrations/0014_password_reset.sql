-- Plan 4c: password reset by email.

-- The email queue gets the member it is for (the rows go with the member), the
-- time after which the message is useless, and the time the worker gave up.
alter table email_queue add column user_id bigint null
	references users (id) on delete cascade deferrable initially immediate;
alter table email_queue add column send_by timestamptz null;
alter table email_queue add column dropped_at timestamptz null;

-- The worker reads the rows that are due: not sent and not dropped.
create index email_queue_due_idx on email_queue (next_attempt_at)
	where sent_at is null and dropped_at is null;

-- One row per reset link. The id is the SHA-256 hex of the token in the link;
-- the token is never stored. epoch is the member's password_epoch when the link
-- was made, so a change of the password makes the link dead.
create table password_resets (
	id         text   primary key,
	user_id    bigint not null
		references users (id) on delete cascade deferrable initially immediate,
	epoch      bigint not null,
	created_at timestamptz not null,
	expires_at timestamptz not null
);

create index password_resets_user_idx on password_resets (user_id);
