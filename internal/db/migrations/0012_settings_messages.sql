-- Plan 4b: member settings and private messages.

-- The date format a member picks. A guest sees 'standard'.
alter table users add column date_format text not null default 'standard'
	check (date_format in ('standard', 'relative', 'iso'));

-- The "About you" fields are plain text. The character limits below are the
-- values of account.MaxLocation, account.MaxOccupation, account.MaxInterests and
-- account.MaxWebsite in internal/account/profile.go. SQL cannot import the Go
-- constants, so a change to one needs the same change here, in a new migration.
alter table users add column location text not null default ''
	check (char_length(location) <= 100);
alter table users add column occupation text not null default ''
	check (char_length(occupation) <= 100);
alter table users add column interests text not null default ''
	check (char_length(interests) <= 200);
alter table users add column website text not null default ''
	check (char_length(website) <= 200);

-- The birthday. The day and the month are set together, and the year needs both.
-- Go checks that the date exists and the age rule (account.CheckBirthday); this
-- table checks the pairing and the plain ranges. 1900 is account.MinBirthYear.
alter table users add column birth_day smallint null;
alter table users add column birth_month smallint null;
alter table users add column birth_year smallint null;
alter table users add constraint users_birthday_check check (
	(birth_day is null) = (birth_month is null)
	and (birth_year is null or birth_day is not null)
	and (birth_day is null or birth_day between 1 and 31)
	and (birth_month is null or birth_month between 1 and 12)
	and (birth_year is null or birth_year >= 1900)
);

-- Today's birthdays: the members with a month and a day.
create index users_birthday_idx on users (birth_month, birth_day)
	where birth_month is not null;

-- The inbox: a member's received messages, newest first.
create index pm_recipients_inbox_idx on pm_recipients (user_id, pm_id desc)
	where not is_deleted;

-- The sent list: a member's sent messages, newest first.
create index pms_sent_idx on pms (author_id, id desc)
	where not author_deleted;
