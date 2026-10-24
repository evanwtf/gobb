package importer

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/account"
	"github.com/evanwtf/gobb/internal/acl"
	"github.com/evanwtf/gobb/markup"
)

// rowErr names the row that caused err, in the form
// "users.jsonl:3 (id 7): signature: message". what names the row's key, for
// example "id 7". field may be empty.
func rowErr(file string, line int, what, field string, err error) error {
	if field != "" {
		return fmt.Errorf("%s.jsonl:%d (%s): %s: %w", file, line, what, field, err)
	}
	return fmt.Errorf("%s.jsonl:%d (%s): %w", file, line, what, err)
}

// idErr is rowErr for a row with an ID.
func idErr(file string, line int, id int64, field string, err error) error {
	return rowErr(file, line, fmt.Sprintf("id %d", id), field, err)
}

// parseTree parses a body with its own flags and the bundle's smiley list, and
// validates the tree.
func parseTree(b bundle.Body, smilies []markup.Smiley) (*markup.Node, error) {
	opt := markup.Options{Markdown: b.Markdown, Smilies: b.Smilies, Autolink: b.Autolink}
	if b.Smilies {
		opt.SmileyList = smilies
	}
	tree := parseBody(b.Source, opt)
	if err := markup.Validate(tree); err != nil {
		return nil, err
	}
	return tree, nil
}

// treeJSON parses a body (see parseTree) and returns the JSON of its tree.
func treeJSON(b bundle.Body, smilies []markup.Smiley) (string, error) {
	tree, err := parseTree(b, smilies)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(tree)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// filePath returns the path of f, or nil for a missing file, for a nullable column.
func filePath(f *bundle.FileRef) any {
	if f == nil {
		return nil
	}
	return f.Path
}

// settingValue maps a bundle setting to the number that acl.Setting stores.
func settingValue(s bundle.Setting) (int16, error) {
	switch s {
	case bundle.Yes:
		return int16(acl.Yes), nil
	case bundle.No:
		return int16(acl.No), nil
	case bundle.Never:
		return int16(acl.Never), nil
	}
	return 0, fmt.Errorf("unknown setting %q", string(s))
}

func loadRanks(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	n := 0
	err := bundle.Each(r.Reader, func(line int, x bundle.Rank) error {
		_, err := tx.Exec(ctx,
			`insert into ranks (id, title, min_posts, special, image_path) values ($1, $2, $3, $4, $5)`,
			x.ID, x.Title, x.MinPosts, x.Special, filePath(x.Image))
		if err != nil {
			return idErr("ranks", line, x.ID, "insert", err)
		}
		n++
		return nil
	})
	return n, err
}

func loadUsers(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	n := 0
	err := bundle.Each(r.Reader, func(line int, u bundle.User) error {
		// An empty signature has no tree.
		var tree any
		if u.Signature.Source != "" {
			t, err := treeJSON(u.Signature, r.smilies)
			if err != nil {
				return idErr("users", line, u.ID, "signature", err)
			}
			tree = t
		}
		_, err := tx.Exec(ctx,
			`insert into users (id, username, username_clean, email, email_clean,
			     password_hash, password_scheme, state, is_owner, email_verified,
			     registered_at, last_visit_at, last_mark_at, post_count, rank_id,
			     avatar_path, signature_source, signature_tree, signature_markdown,
			     signature_smilies, signature_autolink, timezone, color_scheme,
			     notify_on_post)
			 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
			     $16, $17, $18::jsonb, $19, $20, $21, $22, $23, $24)`,
			u.ID, u.Username, account.CleanUsername(u.Username), u.Email, account.CleanEmail(u.Email),
			u.Password.Hash, u.Password.Scheme, u.State, u.Owner, u.EmailVerified,
			u.RegisteredAt, u.LastVisitAt, u.LastMarkAt, u.PostCount, u.RankID,
			filePath(u.Avatar), u.Signature.Source, tree, u.Signature.Markdown,
			u.Signature.Smilies, u.Signature.Autolink, u.Timezone, u.ColorScheme,
			u.NotifyOnPost)
		if err != nil {
			return idErr("users", line, u.ID, "insert", err)
		}
		n++
		return nil
	})
	return n, err
}

func loadGoneUsers(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	n := 0
	err := bundle.Each(r.Reader, func(line int, x bundle.GoneUser) error {
		if _, err := tx.Exec(ctx, `insert into gone_users (id) values ($1)`, x.ID); err != nil {
			return idErr("gone_users", line, x.ID, "insert", err)
		}
		n++
		return nil
	})
	return n, err
}

func loadGroups(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	n := 0
	err := bundle.Each(r.Reader, func(line int, g bundle.Group) error {
		_, err := tx.Exec(ctx,
			`insert into groups (id, name, builtin, kind, description) values ($1, $2, $3, $4, $5)`,
			g.ID, g.Name, g.Builtin, g.Kind, g.Description)
		if err != nil {
			return idErr("groups", line, g.ID, "insert", err)
		}
		n++
		return nil
	})
	return n, err
}

func loadGroupMembers(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	n := 0
	err := bundle.Each(r.Reader, func(line int, m bundle.GroupMember) error {
		_, err := tx.Exec(ctx,
			`insert into group_members (group_id, user_id, is_leader, is_pending) values ($1, $2, $3, $4)`,
			m.GroupID, m.UserID, m.Leader, m.Pending)
		if err != nil {
			return rowErr("group_members", line, fmt.Sprintf("group %d, user %d", m.GroupID, m.UserID), "insert", err)
		}
		n++
		return nil
	})
	return n, err
}

func loadRoles(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	n := 0
	err := bundle.Each(r.Reader, func(line int, x bundle.Role) error {
		_, err := tx.Exec(ctx,
			`insert into acl_roles (id, name, kind, description, position) values ($1, $2, $3, $4, $5)`,
			x.ID, x.Name, x.Kind, x.Description, x.Position)
		if err != nil {
			return idErr("roles", line, x.ID, "insert", err)
		}
		for i, s := range x.Settings {
			v, err := settingValue(s.Setting)
			if err != nil {
				return idErr("roles", line, x.ID, fmt.Sprintf("settings[%d].setting", i), err)
			}
			_, err = tx.Exec(ctx,
				`insert into acl_role_settings (role_id, option, setting) values ($1, $2, $3)`,
				x.ID, s.Option, v)
			if err != nil {
				return idErr("roles", line, x.ID, fmt.Sprintf("settings[%d]", i), err)
			}
		}
		n++
		return nil
	})
	return n, err
}

func loadGrants(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	n := 0
	err := bundle.Each(r.Reader, func(line int, g bundle.Grant) error {
		table, holder := "acl_group_grants", "group_id"
		var id int64
		if g.GroupID != nil {
			id = *g.GroupID
		} else if g.UserID != nil {
			table, holder, id = "acl_user_grants", "user_id", *g.UserID
		}
		what := fmt.Sprintf("%s %d, forum %d", holder, id, g.ForumID)
		var setting any
		if g.Setting != nil {
			v, err := settingValue(*g.Setting)
			if err != nil {
				return rowErr("grants", line, what, "setting", err)
			}
			setting = v
		}
		// The table and column names are literals above.
		_, err := tx.Exec(ctx,
			"insert into "+table+" ("+holder+", forum_id, role_id, option, setting) values ($1, $2, $3, $4, $5)",
			id, g.ForumID, g.RoleID, g.Option, setting)
		if err != nil {
			return rowErr("grants", line, what, "insert", err)
		}
		n++
		return nil
	})
	return n, err
}

func loadForums(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	n := 0
	err := bundle.Each(r.Reader, func(line int, f bundle.Forum) error {
		_, err := tx.Exec(ctx,
			`insert into forums (id, parent_id, kind, name, description, rules, link_url, locked, position)
			 values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			f.ID, f.ParentID, f.Kind, f.Name, f.Description, f.Rules, f.LinkURL, f.Locked, f.Position)
		if err != nil {
			return idErr("forums", line, f.ID, "insert", err)
		}
		n++
		return nil
	})
	return n, err
}

func loadBans(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	n := 0
	err := bundle.Each(r.Reader, func(line int, b bundle.Ban) error {
		_, err := tx.Exec(ctx,
			`insert into bans (kind, user_id, email, ip, reason, reason_shown, created_at, ends_at)
			 values ($1, $2, $3, $4::cidr, $5, $6, $7, $8)`,
			b.Kind, b.UserID, b.Email, b.IP, b.Reason, b.ReasonShown, b.CreatedAt, b.EndsAt)
		if err != nil {
			return rowErr("bans", line, "kind "+b.Kind, "insert", err)
		}
		n++
		return nil
	})
	return n, err
}

func loadSmilies(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	n := 0
	err := bundle.Each(r.Reader, func(line int, s bundle.Smiley) error {
		_, err := tx.Exec(ctx,
			`insert into smilies (code, image_path, description, position) values ($1, $2, $3, $4)`,
			s.Code, s.Image.Path, s.Description, s.Position)
		if err != nil {
			return rowErr("smilies", line, "code "+s.Code, "insert", err)
		}
		n++
		return nil
	})
	return n, err
}

func loadLegacyRoutes(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	n := 0
	err := bundle.Each(r.Reader, func(line int, x bundle.LegacyRoute) error {
		match := x.Match
		if match == nil {
			match = map[string]string{}
		}
		data, err := json.Marshal(match)
		if err != nil {
			return rowErr("legacy_routes", line, "path "+x.Path, "match", err)
		}
		_, err = tx.Exec(ctx,
			`insert into legacy_routes (path, match, id_param, target_kind, target_id,
			     target_slug, offset_param, position)
			 values ($1, $2::jsonb, $3, $4, $5, $6, $7, $8)`,
			x.Path, string(data), x.IDParam, x.TargetKind, x.TargetID,
			x.TargetSlug, x.OffsetParam, x.Position)
		if err != nil {
			return rowErr("legacy_routes", line, "path "+x.Path, "insert", err)
		}
		n++
		return nil
	})
	return n, err
}

func loadLegacyFiles(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	n := 0
	err := bundle.Each(r.Reader, func(line int, x bundle.LegacyFile) error {
		if _, err := tx.Exec(ctx, `insert into legacy_files (prefix, dir) values ($1, $2)`, x.Prefix, x.Dir); err != nil {
			return rowErr("legacy_files", line, "prefix "+x.Prefix, "insert", err)
		}
		n++
		return nil
	})
	return n, err
}

func loadStaticPages(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	n := 0
	err := bundle.Each(r.Reader, func(line int, p bundle.StaticPage) error {
		// A static page always has a tree, even when its source is empty.
		tree, err := treeJSON(p.Body, r.smilies)
		if err != nil {
			return rowErr("static_pages", line, "slug "+p.Slug, "body", err)
		}
		_, err = tx.Exec(ctx,
			`insert into static_pages (slug, title, body_source, body_tree,
			     enable_markdown, enable_smilies, enable_autolink, updated_at)
			 values ($1, $2, $3, $4::jsonb, $5, $6, $7, $8)`,
			p.Slug, p.Title, p.Body.Source, tree,
			p.Body.Markdown, p.Body.Smilies, p.Body.Autolink, p.UpdatedAt)
		if err != nil {
			return rowErr("static_pages", line, "slug "+p.Slug, "insert", err)
		}
		n++
		return nil
	})
	return n, err
}
