package taskhub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

const MaxBodyBytes = 1 << 20

var (
	ErrNotFound = errors.New("task not found")
	ErrConflict = errors.New("task status changed; read it again before updating")
)

type Task struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Status string `json:"status"`
}

type Summary struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type Update struct {
	Title    *string `json:"title,omitempty"`
	Body     *string `json:"body,omitempty"`
	Status   *string `json:"status,omitempty"`
	IfStatus *string `json:"if_status,omitempty"`
}

func ValidStatus(s string) bool {
	return s == "pending" || s == "in_progress" || s == "review" || s == "done"
}

func Validate(title, body, status string) error {
	if strings.TrimSpace(title) == "" || utf8.RuneCountInString(title) > 200 {
		return errors.New("title must contain 1 to 200 characters")
	}
	if strings.ContainsAny(title, "\r\n\t") {
		return errors.New("title must be a single line without tabs")
	}
	if !utf8.ValidString(title) || !utf8.ValidString(body) {
		return errors.New("title and body must be UTF-8")
	}
	if len(body) > MaxBodyBytes {
		return errors.New("body must not exceed 1 MiB")
	}
	if !ValidStatus(status) {
		return errors.New("status must be pending, in_progress, review, or done")
	}
	return nil
}

type Store struct{ db *sql.DB }

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout = 5000;
PRAGMA journal_mode = WAL;
CREATE TABLE IF NOT EXISTS tasks (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 title TEXT NOT NULL,
 body TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL CHECK(status IN ('pending','in_progress','review','done'))
);
CREATE INDEX IF NOT EXISTS tasks_status_id ON tasks(status, id);`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize database: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Add(ctx context.Context, title, body, status string) (Task, error) {
	title = strings.TrimSpace(title)
	if err := Validate(title, body, status); err != nil {
		return Task{}, err
	}
	row := s.db.QueryRowContext(ctx, `INSERT INTO tasks(title, body, status) VALUES(?,?,?) RETURNING id,title,body,status`, title, body, status)
	return scanTask(row)
}

func (s *Store) Get(ctx context.Context, id int64) (Task, error) {
	return scanTask(s.db.QueryRowContext(ctx, `SELECT id,title,body,status FROM tasks WHERE id=?`, id))
}

func scanTask(row *sql.Row) (Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.Title, &t.Body, &t.Status)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return t, err
}

func (s *Store) List(ctx context.Context, status string, after int64, limit int) ([]Summary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,status FROM tasks WHERE id>? AND (?='' OR status=?) ORDER BY id LIMIT ?`, after, status, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []Summary{}
	for rows.Next() {
		var t Summary
		if err := rows.Scan(&t.ID, &t.Title, &t.Status); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (u Update) Validate() error {
	if u.Title == nil && u.Body == nil && u.Status == nil {
		return errors.New("provide title, body, or status to update")
	}
	title, body, status := "valid", "", "pending"
	if u.Title != nil {
		title = strings.TrimSpace(*u.Title)
	}
	if u.Body != nil {
		body = *u.Body
	}
	if u.Status != nil {
		status = *u.Status
	}
	if err := Validate(title, body, status); err != nil {
		return err
	}
	if u.IfStatus != nil && !ValidStatus(*u.IfStatus) {
		return errors.New("invalid if_status")
	}
	return nil
}

func (s *Store) Update(ctx context.Context, id int64, u Update) (Task, error) {
	if err := u.Validate(); err != nil {
		return Task{}, err
	}
	if u.Title != nil {
		title := strings.TrimSpace(*u.Title)
		u.Title = &title
	}
	// The status precondition and mutation are one SQLite statement. Only one
	// client can change a pending task to in_progress with this precondition.
	row := s.db.QueryRowContext(ctx, `UPDATE tasks SET title=COALESCE(?,title),body=COALESCE(?,body),status=COALESCE(?,status)
WHERE id=? AND (? IS NULL OR status=?) RETURNING id,title,body,status`, u.Title, u.Body, u.Status, id, u.IfStatus, u.IfStatus)
	t, err := scanTask(row)
	if errors.Is(err, ErrNotFound) && u.IfStatus != nil {
		if _, getErr := s.Get(ctx, id); getErr != nil {
			return Task{}, getErr
		}
		return Task{}, ErrConflict
	}
	return t, err
}
