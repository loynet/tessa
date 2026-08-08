package state

import (
	"context"
	"database/sql"
	"fmt"

	"tessa/internal/storage"
)

type Store struct {
	db *storage.DB
}

func New(db *storage.DB) (*Store, error) {
	store := &Store{db: db}
	if err := store.initSchema(context.Background()); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) initSchema(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS chatter_cursors (
  name TEXT PRIMARY KEY,
  position INTEGER NOT NULL
);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("init chat schema: %w", err)
	}
	return nil
}

func (s *Store) GetCursor(ctx context.Context, name string) (int64, bool, error) {
	const query = `SELECT position FROM chatter_cursors WHERE name = ?;`

	var position int64
	err := s.db.QueryRowContext(ctx, query, name).Scan(&position)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("query chat cursor: %w", err)
	}
	return position, true, nil
}

func (s *Store) SetCursor(ctx context.Context, name string, position int64) error {
	const statement = `
INSERT INTO chatter_cursors (name, position)
VALUES (?, ?)
ON CONFLICT(name) DO UPDATE SET position = excluded.position;
`

	if _, err := s.db.ExecContext(ctx, statement, name, position); err != nil {
		return fmt.Errorf("set chat cursor: %w", err)
	}
	return nil
}
