package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/bamsammich/docsearch/internal/pgstore/pgdbgen"
)

// Settings are the settings an administrator changed, kept for a docsearch
// started without docsearch.yaml.
type Settings struct {
	admins *Admins
}

// NewSettings reaches the stored settings through db as user.
func NewSettings(db *sql.DB, user string) *Settings {
	return &Settings{admins: NewAdmins(db, user)}
}

// Stored is every kept setting, by key.
func (s *Settings) Stored(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	err := s.admins.run(ctx, func(q *pgdbgen.Queries) error {
		rows, err := q.StoredSettings(ctx)
		for _, row := range rows {
			out[row.Key] = row.Value
		}
		return err
	})
	return out, err
}

// Set keeps value under key, or forgets key when value is nil. The database
// refuses anyone who is not an administrator.
func (s *Settings) Set(ctx context.Context, key string, value *string) error {
	param := sql.NullString{}
	if value != nil {
		param = sql.NullString{String: *value, Valid: true}
	}
	return s.admins.run(ctx, func(q *pgdbgen.Queries) error {
		return q.SetSetting(ctx, pgdbgen.SetSettingParams{Key: key, Value: param})
	})
}

// StoredAtStart is every kept setting, read once as the server starts and
// before any request names a user.
func StoredAtStart(ctx context.Context, db *sql.DB) (map[string]string, error) {
	out := map[string]string{}
	rows, err := pgdbgen.New(db).StoredSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the stored settings: %w", err)
	}
	for _, row := range rows {
		out[row.Key] = row.Value
	}
	return out, nil
}
