-- Search finds words inside angle brackets.
--
-- The PostgreSQL text search parser reads "<like this>" as a markup tag and
-- drops what it holds, so a word that stands only inside angle brackets was never
-- found. Text such as "a < b", "x -> y" and "<3" was never at risk: the parser
-- drops only a "<" that starts a tag. The stored text keeps its brackets, so a
-- snippet shows what the member wrote. Only the search expressions change: they
-- map "<" and ">" to spaces before they parse.

-- The search column of the posts. PostgreSQL 16 cannot change the expression of
-- a generated column in place (ALTER COLUMN ... SET EXPRESSION needs 17), so the
-- column is dropped and added again. That drops posts_search_idx with it, and the
-- index is created again below with the same name. The new column is computed
-- for every row, so the posts that are stored already become searchable too.
alter table posts drop column search;
alter table posts add column search tsvector generated always as (
	to_tsvector('english'::regconfig,
		translate(coalesce(subject, '') || ' ' || coalesce(body_text, ''), '<>', '  '))
) stored;
create index posts_search_idx on posts using gin (search);

-- The title index of the topics. The query writes the same expression.
drop index topics_title_search_idx;
create index topics_title_search_idx on topics
	using gin (to_tsvector('english'::regconfig, translate(title, '<>', '  ')));
