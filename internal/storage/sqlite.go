// Package storage implements SQLite persistence and local attachment file
// storage for Lagrangian using a pure-Go SQLite driver (no CGO required).
package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lagrangian-dev/lagrangian/internal/domain"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

var (
	ErrNotFound = errors.New("record not found")
	ErrConflict = errors.New("record conflict")
)

type Store struct {
	db             *sql.DB
	attachmentsDir string
}

type TaskFilter struct {
	Status          string
	Search          string
	IncludeArchived bool
}

// Open initializes the SQLite database (WAL + foreign keys), runs pending SQL
// migrations, and ensures the attachments directory exists.
func Open(dbPath, attachmentsDir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db parent dir: %w", err)
	}
	if err := os.MkdirAll(attachmentsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create attachments dir: %w", err)
	}

	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	s := &Store{db: db, attachmentsDir: attachmentsDir}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) AttachmentsDir() string {
	return s.attachmentsDir
}

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("init schema_migrations: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version := entry.Name()
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM schema_migrations WHERE version = ?`, version).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		sqlBytes, err := migrationFS.ReadFile("migrations/" + version)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, version, time.Now().UTC().Format(time.RFC3339)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// --- Workstation Nodes ---

func (s *Store) UpsertWorkstation(ctx context.Context, node domain.WorkstationNode) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO workstation_nodes (id, name, os, arch, agent_version, last_heartbeat_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			os = excluded.os,
			arch = excluded.arch,
			agent_version = excluded.agent_version,
			last_heartbeat_at = excluded.last_heartbeat_at
	`, node.ID, node.Name, node.OS, node.Arch, node.AgentVersion,
		node.LastHeartbeatAt.UTC().Format(time.RFC3339Nano),
		node.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) ListWorkstations(ctx context.Context) ([]domain.WorkstationNode, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, os, arch, agent_version, last_heartbeat_at, created_at
		FROM workstation_nodes
		ORDER BY last_heartbeat_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]domain.WorkstationNode, 0)
	for rows.Next() {
		var n domain.WorkstationNode
		var hbRaw, createdRaw string
		if err := rows.Scan(&n.ID, &n.Name, &n.OS, &n.Arch, &n.AgentVersion, &hbRaw, &createdRaw); err != nil {
			return nil, err
		}
		n.LastHeartbeatAt = parseTime(hbRaw)
		n.CreatedAt = parseTime(createdRaw)
		out = append(out, n)
	}
	return out, rows.Err()
}

// --- Tasks ---

func (s *Store) CreateTask(ctx context.Context, t domain.Task) (domain.Task, error) {
	tagsBytes, _ := json.Marshal(nonNilStrings(t.Tags))
	var completedAt any
	if t.CompletedAt != nil {
		completedAt = t.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tasks (
			id, title, description, status, priority, next_action, blocker,
			resume_condition, tags_json, due_date, archived, created_at, updated_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		t.ID, t.Title, t.Description, t.Status, t.Priority, t.NextAction, t.Blocker,
		t.ResumeCondition, string(tagsBytes), t.DueDate, boolToInt(t.Archived),
		t.CreatedAt.UTC().Format(time.RFC3339Nano),
		t.UpdatedAt.UTC().Format(time.RFC3339Nano),
		completedAt,
	)
	if err != nil {
		return domain.Task{}, err
	}
	t.Tags = nonNilStrings(t.Tags)
	return t, nil
}

func (s *Store) UpdateTask(ctx context.Context, t domain.Task) (domain.Task, error) {
	tagsBytes, _ := json.Marshal(nonNilStrings(t.Tags))
	var completedAt any
	if t.CompletedAt != nil {
		completedAt = t.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET
			title = ?, description = ?, status = ?, priority = ?,
			next_action = ?, blocker = ?, resume_condition = ?,
			tags_json = ?, due_date = ?, archived = ?, updated_at = ?, completed_at = ?
		WHERE id = ?
	`,
		t.Title, t.Description, t.Status, t.Priority,
		t.NextAction, t.Blocker, t.ResumeCondition,
		string(tagsBytes), t.DueDate, boolToInt(t.Archived),
		t.UpdatedAt.UTC().Format(time.RFC3339Nano), completedAt, t.ID,
	)
	if err != nil {
		return domain.Task{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.Task{}, ErrNotFound
	}
	t.Tags = nonNilStrings(t.Tags)
	return t, nil
}

func (s *Store) DeleteTask(ctx context.Context, id string) error {
	atts, _ := s.ListAttachments(ctx, id)
	res, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	for _, a := range atts {
		if a.StorageRelPath != "" {
			_ = os.Remove(filepath.Join(s.attachmentsDir, a.StorageRelPath))
		}
	}
	return nil
}

func (s *Store) GetTask(ctx context.Context, id string) (domain.Task, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT
			t.id, t.title, t.description, t.status, t.priority, t.next_action,
			t.blocker, t.resume_condition, t.tags_json, t.due_date, t.archived,
			t.created_at, t.updated_at, t.completed_at,
			(SELECT COUNT(1) FROM task_progress p WHERE p.task_id = t.id),
			(SELECT COUNT(1) FROM commit_links c WHERE c.task_id = t.id AND c.superseded_by = ''),
			(SELECT COUNT(1) FROM repo_bindings r WHERE r.task_id = t.id),
			(SELECT COUNT(1) FROM attachments a WHERE a.task_id = t.id)
		FROM tasks t
		WHERE t.id = ?
	`, id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, ErrNotFound
	}
	return t, err
}

func (s *Store) ListTasks(ctx context.Context, f TaskFilter) ([]domain.Task, error) {
	var conds []string
	var args []any

	if !f.IncludeArchived {
		conds = append(conds, "t.archived = 0")
	}
	if f.Status != "" {
		conds = append(conds, "t.status = ?")
		args = append(args, f.Status)
	}
	if q := strings.TrimSpace(f.Search); q != "" {
		like := "%" + q + "%"
		conds = append(conds, "(t.title LIKE ? OR t.description LIKE ? OR t.next_action LIKE ? OR t.blocker LIKE ? OR t.tags_json LIKE ?)")
		args = append(args, like, like, like, like, like)
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT
			t.id, t.title, t.description, t.status, t.priority, t.next_action,
			t.blocker, t.resume_condition, t.tags_json, t.due_date, t.archived,
			t.created_at, t.updated_at, t.completed_at,
			(SELECT COUNT(1) FROM task_progress p WHERE p.task_id = t.id),
			(SELECT COUNT(1) FROM commit_links c WHERE c.task_id = t.id AND c.superseded_by = ''),
			(SELECT COUNT(1) FROM repo_bindings r WHERE r.task_id = t.id),
			(SELECT COUNT(1) FROM attachments a WHERE a.task_id = t.id)
		FROM tasks t
		%s
		ORDER BY t.archived ASC, t.updated_at DESC
	`, where)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks := make([]domain.Task, 0)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// --- Progress Entries ---

func (s *Store) CreateProgress(ctx context.Context, p domain.ProgressEntry) (domain.ProgressEntry, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO task_progress (id, task_id, title, body, occurred_on, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, p.ID, p.TaskID, p.Title, p.Body, p.OccurredOn, p.Source,
		p.CreatedAt.UTC().Format(time.RFC3339Nano),
		p.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return domain.ProgressEntry{}, err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE tasks SET updated_at = ? WHERE id = ?`, p.UpdatedAt.UTC().Format(time.RFC3339Nano), p.TaskID)
	return p, nil
}

func (s *Store) ListProgress(ctx context.Context, taskID string) ([]domain.ProgressEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, task_id, title, body, occurred_on, source, created_at, updated_at
		FROM task_progress
		WHERE task_id = ?
		ORDER BY occurred_on DESC, created_at DESC
	`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]domain.ProgressEntry, 0)
	for rows.Next() {
		var p domain.ProgressEntry
		var createdRaw, updatedRaw string
		if err := rows.Scan(&p.ID, &p.TaskID, &p.Title, &p.Body, &p.OccurredOn, &p.Source, &createdRaw, &updatedRaw); err != nil {
			return nil, err
		}
		p.CreatedAt = parseTime(createdRaw)
		p.UpdatedAt = parseTime(updatedRaw)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeleteProgress(ctx context.Context, taskID, progressID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM task_progress WHERE id = ? AND task_id = ?`, progressID, taskID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Attachments ---

func (s *Store) SaveAttachment(ctx context.Context, att domain.Attachment, content io.Reader) (domain.Attachment, error) {
	taskDir := filepath.Join(s.attachmentsDir, sanitizeSegment(att.TaskID))
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return domain.Attachment{}, fmt.Errorf("create attachment task dir: %w", err)
	}

	tmpFile, err := os.CreateTemp(taskDir, "upload-*")
	if err != nil {
		return domain.Attachment{}, err
	}
	tmpPath := tmpFile.Name()
	hasher := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(tmpFile, hasher), content)
	closeErr := tmpFile.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(tmpPath)
		if copyErr != nil {
			return domain.Attachment{}, copyErr
		}
		return domain.Attachment{}, closeErr
	}

	sum := hex.EncodeToString(hasher.Sum(nil))
	safeName := sanitizeFilename(att.Filename)
	relPath := filepath.Join(sanitizeSegment(att.TaskID), sum[:12]+"_"+safeName)
	finalPath := filepath.Join(s.attachmentsDir, relPath)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return domain.Attachment{}, err
	}

	att.SHA256 = sum
	att.SizeBytes = size
	att.StorageRelPath = relPath
	att.DownloadURL = "/api/v1/attachments/" + att.ID

	var progID any
	if att.ProgressID != "" {
		progID = att.ProgressID
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO attachments (id, task_id, progress_id, filename, mime_type, size_bytes, sha256, storage_rel_path, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, att.ID, att.TaskID, progID, att.Filename, att.MimeType, att.SizeBytes, att.SHA256, att.StorageRelPath, att.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		_ = os.Remove(finalPath)
		return domain.Attachment{}, err
	}
	return att, nil
}

func (s *Store) ListAttachments(ctx context.Context, taskID string) ([]domain.Attachment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, task_id, COALESCE(progress_id, ''), filename, mime_type, size_bytes, sha256, storage_rel_path, created_at
		FROM attachments
		WHERE task_id = ?
		ORDER BY created_at DESC
	`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]domain.Attachment, 0)
	for rows.Next() {
		var a domain.Attachment
		var createdRaw string
		if err := rows.Scan(&a.ID, &a.TaskID, &a.ProgressID, &a.Filename, &a.MimeType, &a.SizeBytes, &a.SHA256, &a.StorageRelPath, &createdRaw); err != nil {
			return nil, err
		}
		a.CreatedAt = parseTime(createdRaw)
		a.DownloadURL = "/api/v1/attachments/" + a.ID
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetAttachment(ctx context.Context, id string) (domain.Attachment, string, error) {
	var a domain.Attachment
	var createdRaw string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, task_id, COALESCE(progress_id, ''), filename, mime_type, size_bytes, sha256, storage_rel_path, created_at
		FROM attachments WHERE id = ?
	`, id).Scan(&a.ID, &a.TaskID, &a.ProgressID, &a.Filename, &a.MimeType, &a.SizeBytes, &a.SHA256, &a.StorageRelPath, &createdRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Attachment{}, "", ErrNotFound
	}
	if err != nil {
		return domain.Attachment{}, "", err
	}
	a.CreatedAt = parseTime(createdRaw)
	a.DownloadURL = "/api/v1/attachments/" + a.ID
	return a, filepath.Join(s.attachmentsDir, a.StorageRelPath), nil
}

// --- Repo Bindings ---

func (s *Store) CreateRepoBinding(ctx context.Context, b domain.RepoBinding) (domain.RepoBinding, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO repo_bindings (id, task_id, workstation_id, repo_name, repo_path, branch_name, remote_url, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, b.ID, b.TaskID, b.WorkstationID, b.RepoName, b.RepoPath, b.BranchName, b.RemoteURL, b.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return domain.RepoBinding{}, ErrConflict
		}
		return domain.RepoBinding{}, err
	}
	return b, nil
}

func (s *Store) ListRepoBindings(ctx context.Context, taskID string) ([]domain.RepoBinding, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, task_id, workstation_id, repo_name, repo_path, branch_name, remote_url, created_at
		FROM repo_bindings WHERE task_id = ? ORDER BY created_at DESC
	`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]domain.RepoBinding, 0)
	for rows.Next() {
		var b domain.RepoBinding
		var createdRaw string
		if err := rows.Scan(&b.ID, &b.TaskID, &b.WorkstationID, &b.RepoName, &b.RepoPath, &b.BranchName, &b.RemoteURL, &createdRaw); err != nil {
			return nil, err
		}
		b.CreatedAt = parseTime(createdRaw)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) ListAllRepoBindings(ctx context.Context) ([]domain.RepoBinding, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, task_id, workstation_id, repo_name, repo_path, branch_name, remote_url, created_at
		FROM repo_bindings ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]domain.RepoBinding, 0)
	for rows.Next() {
		var b domain.RepoBinding
		var createdRaw string
		if err := rows.Scan(&b.ID, &b.TaskID, &b.WorkstationID, &b.RepoName, &b.RepoPath, &b.BranchName, &b.RemoteURL, &createdRaw); err != nil {
			return nil, err
		}
		b.CreatedAt = parseTime(createdRaw)
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) DeleteRepoBinding(ctx context.Context, taskID, bindingID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM repo_bindings WHERE id = ? AND task_id = ?`, bindingID, taskID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Commit Links & Rebase Supersede ---

func (s *Store) CreateCommitLink(ctx context.Context, c domain.CommitLink) (domain.CommitLink, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO commit_links (
			id, task_id, repo_name, branch_name, sha, title, author,
			committed_at, patch_id, source, superseded_by, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, c.ID, c.TaskID, c.RepoName, c.BranchName, c.SHA, c.Title, c.Author,
		c.CommittedAt, c.PatchID, c.Source, c.SupersededBy, c.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return domain.CommitLink{}, ErrConflict
		}
		return domain.CommitLink{}, err
	}
	return c, nil
}

func (s *Store) GetCommitLinkByRepoSHA(ctx context.Context, repoName, sha string) (domain.CommitLink, error) {
	var c domain.CommitLink
	var createdRaw string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, task_id, repo_name, branch_name, sha, title, author,
		       committed_at, patch_id, source, superseded_by, created_at
		FROM commit_links WHERE repo_name = ? AND sha = ?
	`, repoName, sha).Scan(
		&c.ID, &c.TaskID, &c.RepoName, &c.BranchName, &c.SHA, &c.Title, &c.Author,
		&c.CommittedAt, &c.PatchID, &c.Source, &c.SupersededBy, &createdRaw,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CommitLink{}, ErrNotFound
	}
	if err != nil {
		return domain.CommitLink{}, err
	}
	c.CreatedAt = parseTime(createdRaw)
	return c, nil
}

func (s *Store) ListCommitLinks(ctx context.Context, taskID string, includeSuperseded bool) ([]domain.CommitLink, error) {
	cond := "task_id = ?"
	if !includeSuperseded {
		cond += " AND superseded_by = ''"
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, task_id, repo_name, branch_name, sha, title, author,
		       committed_at, patch_id, source, superseded_by, created_at
		FROM commit_links
		WHERE %s
		ORDER BY committed_at DESC, created_at DESC
	`, cond), taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]domain.CommitLink, 0)
	for rows.Next() {
		var c domain.CommitLink
		var createdRaw string
		if err := rows.Scan(
			&c.ID, &c.TaskID, &c.RepoName, &c.BranchName, &c.SHA, &c.Title, &c.Author,
			&c.CommittedAt, &c.PatchID, &c.Source, &c.SupersededBy, &createdRaw,
		); err != nil {
			return nil, err
		}
		c.CreatedAt = parseTime(createdRaw)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCommitLink(ctx context.Context, taskID, commitID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM commit_links WHERE id = ? AND task_id = ?`, commitID, taskID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SupersedeCommit atomically marks oldCommitID as superseded by newCommit.SHA
// and ensures newCommit is bound to the same task.
func (s *Store) SupersedeCommit(ctx context.Context, oldCommitID string, newCommit domain.CommitLink) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `UPDATE commit_links SET superseded_by = ? WHERE id = ?`, newCommit.SHA, oldCommitID); err != nil {
		return err
	}

	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM commit_links WHERE repo_name = ? AND sha = ?`, newCommit.RepoName, newCommit.SHA).Scan(&existingID)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO commit_links (
				id, task_id, repo_name, branch_name, sha, title, author,
				committed_at, patch_id, source, superseded_by, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?)
		`, newCommit.ID, newCommit.TaskID, newCommit.RepoName, newCommit.BranchName,
			newCommit.SHA, newCommit.Title, newCommit.Author, newCommit.CommittedAt,
			newCommit.PatchID, newCommit.Source, newCommit.CreatedAt.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) CountActiveCommits(ctx context.Context) (int, error) {
	var cnt int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM commit_links WHERE superseded_by = ''`).Scan(&cnt)
	return cnt, err
}

// --- Helpers ---

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTask(row rowScanner) (domain.Task, error) {
	var t domain.Task
	var tagsRaw, createdRaw, updatedRaw string
	var archivedInt int
	var completedRaw sql.NullString
	err := row.Scan(
		&t.ID, &t.Title, &t.Description, &t.Status, &t.Priority, &t.NextAction,
		&t.Blocker, &t.ResumeCondition, &tagsRaw, &t.DueDate, &archivedInt,
		&createdRaw, &updatedRaw, &completedRaw,
		&t.ProgressCount, &t.CommitCount, &t.RepoCount, &t.AttachmentCount,
	)
	if err != nil {
		return domain.Task{}, err
	}
	t.Archived = archivedInt != 0
	t.CreatedAt = parseTime(createdRaw)
	t.UpdatedAt = parseTime(updatedRaw)
	if completedRaw.Valid && completedRaw.String != "" {
		ct := parseTime(completedRaw.String)
		t.CompletedAt = &ct
	}
	if err := json.Unmarshal([]byte(tagsRaw), &t.Tags); err != nil || t.Tags == nil {
		t.Tags = []string{}
	}
	return t, nil
}

func parseTime(v string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t
	}
	return time.Time{}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func sanitizeSegment(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.ReplaceAll(s, "..", "_")
	return s
}

func sanitizeFilename(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "" || base == "." || base == "/" {
		return "file.bin"
	}
	replacer := strings.NewReplacer(" ", "_", "/", "_", "\\", "_", ":", "_", "..", "_")
	return replacer.Replace(base)
}
