package taskhub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

const MaxBodyBytes = 1 << 20

var (
	ErrNotFound = errors.New("task not found")
	ErrConflict = errors.New("task status changed; read it again before updating")
)

type Task struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Status   string `json:"status"`
	Project  string `json:"project"`
	Archived bool   `json:"archived"`
}

type Summary struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Project  string `json:"project"`
	Archived bool   `json:"archived"`
}

type Update struct {
	Title    *string `json:"title,omitempty"`
	Body     *string `json:"body,omitempty"`
	Status   *string `json:"status,omitempty"`
	IfStatus *string `json:"if_status,omitempty"`
	Project  *string `json:"project,omitempty"`
	Archived *bool   `json:"archived,omitempty"`
}

func ValidateProject(project string) error {
	if !utf8.ValidString(project) || utf8.RuneCountInString(project) > 80 || strings.ContainsFunc(project, unicode.IsControl) {
		return errors.New("project must be UTF-8 text of at most 80 characters, without control characters")
	}
	return nil
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
	if err := migrateTasks(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return &Store{db: db}, nil
}

// The immediate transaction serializes schema inspection and modification,
// including when two server processes start against the same old database.
func migrateTasks(db *sql.DB) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, "ROLLBACK")
	for _, column := range []struct{ name, definition string }{
		{"project", "project TEXT NOT NULL DEFAULT ''"},
		{"archived", "archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1))"},
	} {
		var count int
		if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_table_info('tasks') WHERE name=?", column.name).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			if _, err = conn.ExecContext(ctx, "ALTER TABLE tasks ADD COLUMN "+column.definition); err != nil {
				return err
			}
		}
	}
	if _, err = conn.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS tasks_archived_project_status_id ON tasks(archived,project,status,id); CREATE INDEX IF NOT EXISTS tasks_archived_id ON tasks(archived,id)"); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Add(ctx context.Context, title, body, status, project string) (Task, error) {
	title = strings.TrimSpace(title)
	project = strings.TrimSpace(project)
	if err := Validate(title, body, status); err != nil {
		return Task{}, err
	}
	if err := ValidateProject(project); err != nil {
		return Task{}, err
	}
	row := s.db.QueryRowContext(ctx, `INSERT INTO tasks(title, body, status, project) VALUES(?,?,?,?) RETURNING id,title,body,status,project,archived`, title, body, status, project)
	return scanTask(row)
}

func (s *Store) Get(ctx context.Context, id int64, includeArchived bool) (Task, error) {
	query := `SELECT id,title,body,status,project,archived FROM tasks WHERE id=?`
	if !includeArchived {
		query += " AND archived=0"
	}
	return scanTask(s.db.QueryRowContext(ctx, query, id))
}

func scanTask(row *sql.Row) (Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.Title, &t.Body, &t.Status, &t.Project, &t.Archived)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return t, err
}

func (s *Store) List(ctx context.Context, status string, project *string, archived bool, after int64, limit int) ([]Summary, error) {
	query := `SELECT id,title,status,project,archived FROM tasks WHERE archived=? AND id>? AND (?='' OR status=?)`
	args := []any{archived, after, status, status}
	if project != nil {
		query += " AND project=?"
		args = append(args, *project)
	}
	query += " ORDER BY id LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []Summary{}
	for rows.Next() {
		var t Summary
		if err := rows.Scan(&t.ID, &t.Title, &t.Status, &t.Project, &t.Archived); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (u Update) Validate() error {
	if u.Title == nil && u.Body == nil && u.Status == nil && u.Project == nil && u.Archived == nil {
		return errors.New("provide title, body, status, project, or archived to update")
	}
	if u.Archived != nil && (u.Title != nil || u.Body != nil || u.Status != nil || u.Project != nil) {
		return errors.New("archive or restore separately from editing task fields")
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
	if u.Project != nil {
		return ValidateProject(strings.TrimSpace(*u.Project))
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
	if u.Project != nil {
		project := strings.TrimSpace(*u.Project)
		u.Project = &project
	}
	// The status precondition and mutation are one SQLite statement. Only one
	// client can change a pending task to in_progress with this precondition.
	row := s.db.QueryRowContext(ctx, `UPDATE tasks SET title=COALESCE(?,title),body=COALESCE(?,body),status=COALESCE(?,status),project=COALESCE(?,project),archived=COALESCE(?,archived)
WHERE id=? AND (? IS NULL OR status=?) AND (? IS NOT NULL OR archived=0) RETURNING id,title,body,status,project,archived`, u.Title, u.Body, u.Status, u.Project, u.Archived, id, u.IfStatus, u.IfStatus, u.Archived)
	t, err := scanTask(row)
	if errors.Is(err, ErrNotFound) && u.IfStatus != nil {
		if _, getErr := s.Get(ctx, id, u.Archived != nil); getErr != nil {
			return Task{}, getErr
		}
		return Task{}, ErrConflict
	}
	return t, err
}
