package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/lagrangian-dev/lagrangian/internal/domain"
)

// --- Integration Connections ---

func (s *Store) CreateConnection(ctx context.Context, c domain.IntegrationConnection) (domain.IntegrationConnection, error) {
	optsBytes, _ := json.Marshal(nonNilMap(c.Options))
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO integration_connections (
			id, kind, driver, name, base_url, auth_type, username, secret_ref,
			options_json, enabled, last_checked_at, last_error, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, c.ID, c.Kind, c.Driver, c.Name, c.BaseURL, c.AuthType, c.Username, c.SecretRef,
		string(optsBytes), boolToInt(c.Enabled), c.LastCheckedAt, c.LastError,
		c.CreatedAt.UTC().Format(time.RFC3339Nano), c.UpdatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return domain.IntegrationConnection{}, err
	}
	c.Options = nonNilMap(c.Options)
	c.HasSecret = c.SecretRef != ""
	return c, nil
}

func (s *Store) UpdateConnection(ctx context.Context, c domain.IntegrationConnection) (domain.IntegrationConnection, error) {
	optsBytes, _ := json.Marshal(nonNilMap(c.Options))
	res, err := s.db.ExecContext(ctx, `
		UPDATE integration_connections SET
			name = ?, base_url = ?, auth_type = ?, username = ?, secret_ref = ?,
			options_json = ?, enabled = ?, last_checked_at = ?, last_error = ?, updated_at = ?
		WHERE id = ?
	`, c.Name, c.BaseURL, c.AuthType, c.Username, c.SecretRef,
		string(optsBytes), boolToInt(c.Enabled), c.LastCheckedAt, c.LastError,
		c.UpdatedAt.UTC().Format(time.RFC3339Nano), c.ID)
	if err != nil {
		return domain.IntegrationConnection{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.IntegrationConnection{}, ErrNotFound
	}
	c.Options = nonNilMap(c.Options)
	c.HasSecret = c.SecretRef != ""
	return c, nil
}

func (s *Store) GetConnection(ctx context.Context, id string) (domain.IntegrationConnection, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, kind, driver, name, base_url, auth_type, username, secret_ref,
		       options_json, enabled, last_checked_at, last_error, created_at, updated_at
		FROM integration_connections WHERE id = ?
	`, id)
	c, err := scanConnection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IntegrationConnection{}, ErrNotFound
	}
	return c, err
}

func (s *Store) ListConnections(ctx context.Context, kind string) ([]domain.IntegrationConnection, error) {
	query := `
		SELECT id, kind, driver, name, base_url, auth_type, username, secret_ref,
		       options_json, enabled, last_checked_at, last_error, created_at, updated_at
		FROM integration_connections
	`
	var args []any
	if kind != "" {
		query += " WHERE kind = ?"
		args = append(args, kind)
	}
	query += " ORDER BY created_at DESC"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]domain.IntegrationConnection, 0)
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteConnection(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM integration_connections WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanConnection(row rowScanner) (domain.IntegrationConnection, error) {
	var c domain.IntegrationConnection
	var optsRaw, createdRaw, updatedRaw string
	var enabledInt int
	err := row.Scan(
		&c.ID, &c.Kind, &c.Driver, &c.Name, &c.BaseURL, &c.AuthType, &c.Username,
		&c.SecretRef, &optsRaw, &enabledInt, &c.LastCheckedAt, &c.LastError, &createdRaw, &updatedRaw,
	)
	if err != nil {
		return domain.IntegrationConnection{}, err
	}
	c.Enabled = enabledInt != 0
	c.HasSecret = c.SecretRef != ""
	c.CreatedAt = parseTime(createdRaw)
	c.UpdatedAt = parseTime(updatedRaw)
	if err := json.Unmarshal([]byte(optsRaw), &c.Options); err != nil || c.Options == nil {
		c.Options = map[string]string{}
	}
	return c, nil
}

// --- Remote Links ---

func (s *Store) UpsertRemoteLink(ctx context.Context, l domain.RemoteLink) (domain.RemoteLink, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO remote_links (
			id, task_id, connection_id, remote_kind, remote_key, title, remote_state, web_url, synced_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id, connection_id, remote_kind, remote_key) DO UPDATE SET
			title = excluded.title,
			remote_state = excluded.remote_state,
			web_url = excluded.web_url,
			synced_at = excluded.synced_at
	`, l.ID, l.TaskID, l.ConnectionID, l.RemoteKind, l.RemoteKey, l.Title, l.RemoteState, l.WebURL,
		l.SyncedAt.UTC().Format(time.RFC3339Nano), l.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return domain.RemoteLink{}, err
	}
	// Reload canonical ID in case of conflict update.
	row := s.db.QueryRowContext(ctx, `
		SELECT id, task_id, connection_id, remote_kind, remote_key, title, remote_state, web_url, synced_at, created_at
		FROM remote_links WHERE task_id = ? AND connection_id = ? AND remote_kind = ? AND remote_key = ?
	`, l.TaskID, l.ConnectionID, l.RemoteKind, l.RemoteKey)
	return scanRemoteLink(row)
}

func (s *Store) ListRemoteLinks(ctx context.Context, taskID string) ([]domain.RemoteLink, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, task_id, connection_id, remote_kind, remote_key, title, remote_state, web_url, synced_at, created_at
		FROM remote_links WHERE task_id = ? ORDER BY created_at DESC
	`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]domain.RemoteLink, 0)
	for rows.Next() {
		l, err := scanRemoteLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) ListAllRemoteLinks(ctx context.Context) ([]domain.RemoteLink, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, task_id, connection_id, remote_kind, remote_key, title, remote_state, web_url, synced_at, created_at
		FROM remote_links ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]domain.RemoteLink, 0)
	for rows.Next() {
		l, err := scanRemoteLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) DeleteRemoteLink(ctx context.Context, taskID, linkID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM remote_links WHERE id = ? AND task_id = ?`, linkID, taskID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanRemoteLink(row rowScanner) (domain.RemoteLink, error) {
	var l domain.RemoteLink
	var syncedRaw, createdRaw string
	err := row.Scan(&l.ID, &l.TaskID, &l.ConnectionID, &l.RemoteKind, &l.RemoteKey,
		&l.Title, &l.RemoteState, &l.WebURL, &syncedRaw, &createdRaw)
	if err != nil {
		return domain.RemoteLink{}, err
	}
	l.SyncedAt = parseTime(syncedRaw)
	l.CreatedAt = parseTime(createdRaw)
	return l, nil
}

// --- Issue Followups (Local Pause / Acceptance State) ---

func (s *Store) UpsertIssueFollowup(ctx context.Context, f domain.IssueFollowup) (domain.IssueFollowup, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO issue_followups (
			id, connection_id, issue_key, title, web_url, remote_state, local_state, reason, revision, updated_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(connection_id, issue_key) DO UPDATE SET
			title = CASE WHEN excluded.title != '' THEN excluded.title ELSE issue_followups.title END,
			web_url = CASE WHEN excluded.web_url != '' THEN excluded.web_url ELSE issue_followups.web_url END,
			remote_state = CASE WHEN excluded.remote_state != '' THEN excluded.remote_state ELSE issue_followups.remote_state END,
			local_state = excluded.local_state,
			reason = excluded.reason,
			revision = CASE WHEN excluded.revision != '' THEN excluded.revision ELSE issue_followups.revision END,
			updated_at = excluded.updated_at
	`, f.ID, f.ConnectionID, f.IssueKey, f.Title, f.WebURL, f.RemoteState, f.LocalState, f.Reason, f.Revision,
		f.UpdatedAt.UTC().Format(time.RFC3339Nano), f.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return domain.IssueFollowup{}, err
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, connection_id, issue_key, title, web_url, remote_state, local_state, reason, revision, updated_at, created_at
		FROM issue_followups WHERE connection_id = ? AND issue_key = ?
	`, f.ConnectionID, f.IssueKey)
	return scanIssueFollowup(row)
}

func (s *Store) ListIssueFollowups(ctx context.Context, connectionID, localState string) ([]domain.IssueFollowup, error) {
	var conds []string
	var args []any
	if connectionID != "" {
		conds = append(conds, "connection_id = ?")
		args = append(args, connectionID)
	}
	if localState != "" {
		conds = append(conds, "local_state = ?")
		args = append(args, localState)
	}
	query := `
		SELECT id, connection_id, issue_key, title, web_url, remote_state, local_state, reason, revision, updated_at, created_at
		FROM issue_followups
	`
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += " ORDER BY updated_at DESC"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]domain.IssueFollowup, 0)
	for rows.Next() {
		f, err := scanIssueFollowup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func scanIssueFollowup(row rowScanner) (domain.IssueFollowup, error) {
	var f domain.IssueFollowup
	var updatedRaw, createdRaw string
	err := row.Scan(&f.ID, &f.ConnectionID, &f.IssueKey, &f.Title, &f.WebURL,
		&f.RemoteState, &f.LocalState, &f.Reason, &f.Revision, &updatedRaw, &createdRaw)
	if err != nil {
		return domain.IssueFollowup{}, err
	}
	f.UpdatedAt = parseTime(updatedRaw)
	f.CreatedAt = parseTime(createdRaw)
	return f, nil
}

// --- Knowledge Bases & FTS5 Index ---

func (s *Store) CreateKnowledgeBase(ctx context.Context, kb domain.KnowledgeBase) (domain.KnowledgeBase, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO knowledge_bases (
			id, name, source_type, location, branch, secret_ref, readonly, mirror_dir,
			last_commit_sha, last_synced_at, last_error, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, kb.ID, kb.Name, kb.SourceType, kb.Location, kb.Branch, kb.SecretRef, boolToInt(kb.ReadOnly),
		kb.MirrorDir, kb.LastCommitSHA, kb.LastSyncedAt, kb.LastError,
		kb.CreatedAt.UTC().Format(time.RFC3339Nano), kb.UpdatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return domain.KnowledgeBase{}, err
	}
	kb.HasSecret = kb.SecretRef != ""
	return kb, nil
}

func (s *Store) UpdateKnowledgeBaseSync(ctx context.Context, id, commitSHA, syncedAt, lastErr string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE knowledge_bases SET last_commit_sha = ?, last_synced_at = ?, last_error = ?, updated_at = ?
		WHERE id = ?
	`, commitSHA, syncedAt, lastErr, time.Now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetKnowledgeBase(ctx context.Context, id string) (domain.KnowledgeBase, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT b.id, b.name, b.source_type, b.location, b.branch, b.secret_ref, b.readonly, b.mirror_dir,
		       b.last_commit_sha, b.last_synced_at, b.last_error, b.created_at, b.updated_at,
		       (SELECT COUNT(1) FROM knowledge_docs d WHERE d.kb_id = b.id)
		FROM knowledge_bases b WHERE b.id = ?
	`, id)
	kb, err := scanKnowledgeBase(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.KnowledgeBase{}, ErrNotFound
	}
	return kb, err
}

func (s *Store) ListKnowledgeBases(ctx context.Context) ([]domain.KnowledgeBase, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT b.id, b.name, b.source_type, b.location, b.branch, b.secret_ref, b.readonly, b.mirror_dir,
		       b.last_commit_sha, b.last_synced_at, b.last_error, b.created_at, b.updated_at,
		       (SELECT COUNT(1) FROM knowledge_docs d WHERE d.kb_id = b.id)
		FROM knowledge_bases b ORDER BY b.created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]domain.KnowledgeBase, 0)
	for rows.Next() {
		kb, err := scanKnowledgeBase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, kb)
	}
	return out, rows.Err()
}

func (s *Store) DeleteKnowledgeBase(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM knowledge_fts WHERE kb_id = ?`, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM knowledge_bases WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func scanKnowledgeBase(row rowScanner) (domain.KnowledgeBase, error) {
	var kb domain.KnowledgeBase
	var roInt int
	var createdRaw, updatedRaw string
	err := row.Scan(
		&kb.ID, &kb.Name, &kb.SourceType, &kb.Location, &kb.Branch, &kb.SecretRef, &roInt, &kb.MirrorDir,
		&kb.LastCommitSHA, &kb.LastSyncedAt, &kb.LastError, &createdRaw, &updatedRaw, &kb.DocCount,
	)
	if err != nil {
		return domain.KnowledgeBase{}, err
	}
	kb.ReadOnly = roInt != 0
	kb.HasSecret = kb.SecretRef != ""
	kb.CreatedAt = parseTime(createdRaw)
	kb.UpdatedAt = parseTime(updatedRaw)
	return kb, nil
}

type IndexedDoc struct {
	Doc  domain.KnowledgeDoc
	Body string
}

// ReplaceKnowledgeDocs atomically replaces all indexed documents and FTS5 rows for kbID.
func (s *Store) ReplaceKnowledgeDocs(ctx context.Context, kbID string, docs []IndexedDoc) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM knowledge_docs WHERE kb_id = ?`, kbID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM knowledge_fts WHERE kb_id = ?`, kbID); err != nil {
		return err
	}
	for _, item := range docs {
		d := item.Doc
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_docs (id, kb_id, rel_path, title, content_sha256, size_bytes, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, d.ID, d.KBID, d.RelPath, d.Title, d.ContentSHA256, d.SizeBytes, d.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_fts (doc_id, kb_id, rel_path, title, body)
			VALUES (?, ?, ?, ?, ?)
		`, d.ID, d.KBID, d.RelPath, d.Title, item.Body); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpsertKnowledgeDoc updates or inserts a single document and its FTS5 entry.
func (s *Store) UpsertKnowledgeDoc(ctx context.Context, item IndexedDoc) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	d := item.Doc
	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM knowledge_docs WHERE kb_id = ? AND rel_path = ?`, d.KBID, d.RelPath).Scan(&existingID)
	if err == nil && existingID != "" {
		d.ID = existingID
		if _, err := tx.ExecContext(ctx, `DELETE FROM knowledge_fts WHERE doc_id = ?`, existingID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE knowledge_docs SET title = ?, content_sha256 = ?, size_bytes = ?, updated_at = ?
			WHERE id = ?
		`, d.Title, d.ContentSHA256, d.SizeBytes, d.UpdatedAt.UTC().Format(time.RFC3339Nano), existingID); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO knowledge_docs (id, kb_id, rel_path, title, content_sha256, size_bytes, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, d.ID, d.KBID, d.RelPath, d.Title, d.ContentSHA256, d.SizeBytes, d.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO knowledge_fts (doc_id, kb_id, rel_path, title, body)
		VALUES (?, ?, ?, ?, ?)
	`, d.ID, d.KBID, d.RelPath, d.Title, item.Body); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListKnowledgeDocs(ctx context.Context, kbID string) ([]domain.KnowledgeDoc, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, kb_id, rel_path, title, content_sha256, size_bytes, updated_at
		FROM knowledge_docs WHERE kb_id = ? ORDER BY rel_path ASC
	`, kbID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]domain.KnowledgeDoc, 0)
	for rows.Next() {
		var d domain.KnowledgeDoc
		var updatedRaw string
		if err := rows.Scan(&d.ID, &d.KBID, &d.RelPath, &d.Title, &d.ContentSHA256, &d.SizeBytes, &updatedRaw); err != nil {
			return nil, err
		}
		d.UpdatedAt = parseTime(updatedRaw)
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListKnowledgeDocBodies returns all indexed documents and their raw bodies for a KB
// so link graphs can be built rapidly from SQLite without rescanning disk.
func (s *Store) ListKnowledgeDocBodies(ctx context.Context, kbID string) ([]IndexedDoc, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT doc_id, kb_id, rel_path, title, body
		FROM knowledge_fts WHERE kb_id = ? ORDER BY rel_path ASC
	`, kbID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]IndexedDoc, 0)
	for rows.Next() {
		var item IndexedDoc
		if err := rows.Scan(&item.Doc.ID, &item.Doc.KBID, &item.Doc.RelPath, &item.Doc.Title, &item.Body); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// SearchKnowledge performs full-text search via SQLite FTS5, falling back cleanly
// to substring matching for CJK terms that FTS5 unicode61 does not segment by word.
func (s *Store) SearchKnowledge(ctx context.Context, query, kbID string, limit int) ([]domain.KnowledgeSearchHit, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return []domain.KnowledgeSearchHit{}, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}

	// Quote user query for safe FTS5 MATCH syntax.
	ftsExpr := `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
	var args []any
	sqlStr := `
		SELECT f.doc_id, f.kb_id, b.name, f.rel_path, f.title,
		       snippet(knowledge_fts, 4, '[', ']', '…', 18)
		FROM knowledge_fts f
		JOIN knowledge_bases b ON b.id = f.kb_id
		WHERE knowledge_fts MATCH ?
	`
	args = append(args, ftsExpr)
	if kbID != "" {
		sqlStr += " AND f.kb_id = ?"
		args = append(args, kbID)
	}
	sqlStr += " ORDER BY bm25(knowledge_fts) LIMIT ?"
	args = append(args, limit)

	hits, err := s.runKnowledgeSearchRows(ctx, sqlStr, args...)
	if err == nil && len(hits) > 0 {
		return hits, nil
	}

	// Fallback substring match for Chinese / partial tokens
	like := "%" + q + "%"
	args = []any{like, like, like}
	fallbackSQL := `
		SELECT f.doc_id, f.kb_id, b.name, f.rel_path, f.title,
		       substr(f.body, 1, 160)
		FROM knowledge_fts f
		JOIN knowledge_bases b ON b.id = f.kb_id
		WHERE (f.title LIKE ? OR f.rel_path LIKE ? OR f.body LIKE ?)
	`
	if kbID != "" {
		fallbackSQL += " AND f.kb_id = ?"
		args = append(args, kbID)
	}
	fallbackSQL += " LIMIT ?"
	args = append(args, limit)
	return s.runKnowledgeSearchRows(ctx, fallbackSQL, args...)
}

func (s *Store) runKnowledgeSearchRows(ctx context.Context, query string, args ...any) ([]domain.KnowledgeSearchHit, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.KnowledgeSearchHit, 0)
	for rows.Next() {
		var h domain.KnowledgeSearchHit
		if err := rows.Scan(&h.DocID, &h.KBID, &h.KBName, &h.RelPath, &h.Title, &h.Snippet); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
