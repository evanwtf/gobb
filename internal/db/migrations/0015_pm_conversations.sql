-- Private messages join in conversations. conversation_id is the ID of the first
-- message of the conversation. A message that starts one holds its own ID. Every
-- existing message starts its own conversation; `gobb messages thread` joins the
-- old replies to the messages they answer.
--
-- No foreign key: a row of pms is never removed, and the first message of a
-- conversation may be one that no party sees any more.
alter table pms add column conversation_id bigint;
update pms set conversation_id = id;
alter table pms alter column conversation_id set not null;
create index pms_conversation on pms (conversation_id, id);
