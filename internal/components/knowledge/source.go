// Package knowledge implements generic local-directory and remote-Git knowledge
// base synchronization, safe document read/write, auto-commit/rebase/push for
// writable remote Git repositories, and Markdown title extraction.
package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lagrangian-dev/lagrangian/internal/domain"
	"github.com/lagrangian-dev/lagrangian/internal/storage"
)

var (
	kbLocksMu sync.Mutex
	kbLocks   = map[string]*sync.Mutex{}
)

func lockKB(kbID string) func() {
	kbLocksMu.Lock()
	mu, ok := kbLocks[kbID]
	if !ok {
		mu = &sync.Mutex{}
		kbLocks[kbID] = mu
	}
	kbLocksMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

var allowedExts = map[string]bool{
	".md":       true,
	".markdown": true,
	".txt":      true,
}

var skipDirs = map[string]bool{
	".git":         true,
	".obsidian":    true,
	"node_modules": true,
	"vendor":       true,
	".venv":        true,
	"dist":         true,
	"build":        true,
}

// RootDir returns the active local directory that holds the knowledge base files:
// Location for local_dir, MirrorDir for remote_git.
func RootDir(kb domain.KnowledgeBase) string {
	if kb.SourceType == "remote_git" {
		return kb.MirrorDir
	}
	return kb.Location
}

// SyncRemoteGit clones or updates a remote Git repository into kb.MirrorDir.
// Credentials (if any) are supplied via temporary GIT_ASKPASS so secrets never enter .git/config.
func SyncRemoteGit(ctx context.Context, kb domain.KnowledgeBase, secret string) (string, error) {
	if kb.SourceType != "remote_git" {
		return "", nil
	}
	unlock := lockKB(kb.ID)
	defer unlock()
	return syncRemoteGitLocked(ctx, kb, secret)
}

func syncRemoteGitLocked(ctx context.Context, kb domain.KnowledgeBase, secret string) (string, error) {
	if strings.TrimSpace(kb.MirrorDir) == "" {
		return "", errors.New("知识库镜像目录未设置")
	}
	if err := os.MkdirAll(filepath.Dir(kb.MirrorDir), 0o755); err != nil {
		return "", err
	}

	env, cleanup, err := gitAuthEnv(secret)
	if err != nil {
		return "", err
	}
	defer cleanup()

	gitDir := filepath.Join(kb.MirrorDir, ".git")
	branch := strings.TrimSpace(kb.Branch)
	if _, err := os.Stat(gitDir); errors.Is(err, os.ErrNotExist) {
		args := []string{"clone"}
		if kb.ReadOnly {
			args = append(args, "--depth", "1")
		}
		if branch != "" {
			args = append(args, "--branch", branch)
		}
		args = append(args, kb.Location, kb.MirrorDir)
		if _, err := runGitEnv(ctx, env, args...); err != nil {
			return "", fmt.Errorf("克隆远端知识库失败: %w", err)
		}
	} else {
		fetchArgs := []string{"-C", kb.MirrorDir, "fetch", "origin"}
		if kb.ReadOnly {
			fetchArgs = []string{"-C", kb.MirrorDir, "fetch", "--depth", "1", "origin"}
		}
		if branch != "" {
			fetchArgs = append(fetchArgs, branch)
		}
		if _, err := runGitEnv(ctx, env, fetchArgs...); err != nil {
			return "", fmt.Errorf("拉取远端知识库失败: %w", err)
		}
		if branch != "" {
			if _, err := runGitEnv(ctx, env, "-C", kb.MirrorDir, "checkout", "-B", branch, "FETCH_HEAD"); err != nil {
				return "", fmt.Errorf("切换远端知识库分支 %s 失败: %w", branch, err)
			}
		} else {
			if _, err := runGitEnv(ctx, env, "-C", kb.MirrorDir, "reset", "--hard", "FETCH_HEAD"); err != nil {
				return "", fmt.Errorf("更新远端知识库工作区失败: %w", err)
			}
		}
	}

	sha, err := runGitEnv(ctx, env, "-C", kb.MirrorDir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sha), nil
}

// RemotePushResult describes the outcome of saving and pushing a doc in a writable remote_git KB.
type RemotePushResult struct {
	Doc          storage.IndexedDoc
	CommitSHA    string
	PulledRemote bool
}

// WriteAndPushRemoteGit writes a document in a writable remote_git KB, commits it,
// safely rebases on top of any newer remote commits (aborting & rolling back on conflict),
// and pushes back to the tracked remote branch.
func WriteAndPushRemoteGit(
	ctx context.Context,
	kb domain.KnowledgeBase,
	relPath, content, secret, authorName, authorEmail string,
) (RemotePushResult, error) {
	if kb.SourceType != "remote_git" {
		return RemotePushResult{}, errors.New("仅适用于 remote_git 知识库")
	}
	if kb.ReadOnly {
		return RemotePushResult{}, errors.New("当前远端知识库为只读模式，不允许写入或推送")
	}

	unlock := lockKB(kb.ID)
	defer unlock()

	// Ensure mirror is initialized
	gitDir := filepath.Join(kb.MirrorDir, ".git")
	if _, err := os.Stat(gitDir); errors.Is(err, os.ErrNotExist) {
		if _, err := syncRemoteGitLocked(ctx, kb, secret); err != nil {
			return RemotePushResult{}, err
		}
	}

	env, cleanup, err := gitAuthEnv(secret)
	if err != nil {
		return RemotePushResult{}, err
	}
	defer cleanup()

	// If mirror was previously shallow-cloned, unshallow so rebase/push works reliably.
	if _, err := os.Stat(filepath.Join(gitDir, "shallow")); err == nil {
		_, _ = runGitEnv(ctx, env, "-C", kb.MirrorDir, "fetch", "--unshallow", "origin")
	}

	branch := strings.TrimSpace(kb.Branch)
	if branch == "" {
		if bOut, err := runGitEnv(ctx, env, "-C", kb.MirrorDir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
			b := strings.TrimSpace(bOut)
			if b != "" && b != "HEAD" {
				branch = b
			}
		}
	}
	if branch == "" {
		branch = "main"
	}

	preSHAOut, err := runGitEnv(ctx, env, "-C", kb.MirrorDir, "rev-parse", "HEAD")
	if err != nil {
		return RemotePushResult{}, fmt.Errorf("读取镜像仓库 HEAD 失败: %w", err)
	}
	preSHA := strings.TrimSpace(preSHAOut)

	// Fetch latest remote branch state before committing & rebasing.
	remoteRef := "origin/" + branch
	if _, err := runGitEnv(ctx, env, "-C", kb.MirrorDir, "fetch", "origin", "+refs/heads/"+branch+":refs/remotes/origin/"+branch); err != nil {
		return RemotePushResult{}, fmt.Errorf("拉取远端分支 %s 失败: %w", branch, err)
	}

	// Write the edited file into the mirror worktree.
	indexed, err := WriteDoc(kb, relPath, content)
	if err != nil {
		return RemotePushResult{}, err
	}

	// Check whether file actually changed compared to HEAD.
	statusOut, err := runGitEnv(ctx, env, "-C", kb.MirrorDir, "status", "--porcelain", "--", indexed.Doc.RelPath)
	if err != nil {
		return RemotePushResult{}, fmt.Errorf("检查仓库变更状态失败: %w", err)
	}
	if strings.TrimSpace(statusOut) == "" {
		return RemotePushResult{Doc: indexed, CommitSHA: preSHA}, nil
	}

	if _, err := runGitEnv(ctx, env, "-C", kb.MirrorDir, "add", "--", indexed.Doc.RelPath); err != nil {
		_, _ = runGitEnv(ctx, env, "-C", kb.MirrorDir, "reset", "--hard", preSHA)
		return RemotePushResult{}, fmt.Errorf("暂存文档变更失败: %w", err)
	}

	name := strings.TrimSpace(authorName)
	if name == "" {
		name = "Lagrangian"
	}
	email := strings.TrimSpace(authorEmail)
	if email == "" {
		email = "lagrangian@local"
	}
	commitMsg := fmt.Sprintf("docs(kb): update %s", indexed.Doc.RelPath)
	if _, err := runGitEnv(ctx, env,
		"-C", kb.MirrorDir,
		"-c", "user.name="+name,
		"-c", "user.email="+email,
		"commit", "-m", commitMsg, "--", indexed.Doc.RelPath,
	); err != nil {
		_, _ = runGitEnv(ctx, env, "-C", kb.MirrorDir, "reset", "--hard", preSHA)
		return RemotePushResult{}, fmt.Errorf("提交文档变更失败: %w", err)
	}

	// Check if remote branch has commits ahead of our base.
	pulledRemote := false
	behindOut, err := runGitEnv(ctx, env, "-C", kb.MirrorDir, "rev-list", "--count", preSHA+".."+remoteRef)
	if err == nil && strings.TrimSpace(behindOut) != "0" && strings.TrimSpace(behindOut) != "" {
		pulledRemote = true
		if _, rbErr := runGitEnv(ctx, env,
			"-C", kb.MirrorDir,
			"-c", "user.name="+name,
			"-c", "user.email="+email,
			"rebase", remoteRef,
		); rbErr != nil {
			_, _ = runGitEnv(ctx, env, "-C", kb.MirrorDir, "rebase", "--abort")
			_, _ = runGitEnv(ctx, env, "-C", kb.MirrorDir, "reset", "--hard", preSHA)
			return RemotePushResult{}, errors.New("远端仓库存在同一文件的内容冲突，已自动中止变基并回滚本地改动，请先点击「同步」获取远端最新版本后再编辑")
		}
	}

	// Push to remote branch (never force-push).
	if _, pushErr := runGitEnv(ctx, env, "-C", kb.MirrorDir, "push", "origin", "HEAD:refs/heads/"+branch); pushErr != nil {
		_, _ = runGitEnv(ctx, env, "-C", kb.MirrorDir, "reset", "--hard", preSHA)
		return RemotePushResult{}, fmt.Errorf("推送至远端仓库失败，已回滚本地镜像（请检查写入权限或先同步远端最新提交）: %w", pushErr)
	}

	newSHAOut, err := runGitEnv(ctx, env, "-C", kb.MirrorDir, "rev-parse", "HEAD")
	if err != nil {
		return RemotePushResult{}, err
	}
	return RemotePushResult{
		Doc:          indexed,
		CommitSHA:    strings.TrimSpace(newSHAOut),
		PulledRemote: pulledRemote,
	}, nil
}

// ScanDocs walks rootDir and extracts indexable Markdown/text documents.
func ScanDocs(kbID, rootDir string) ([]storage.IndexedDoc, error) {
	info, err := os.Stat(rootDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("知识库目录不可访问: %s", rootDir)
	}

	var docs []storage.IndexedDoc
	err = filepath.WalkDir(rootDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if p != rootDir && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if !allowedExts[ext] {
			return nil
		}
		rel, err := filepath.Rel(rootDir, p)
		if err != nil {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil || len(raw) > 2<<20 { // skip oversized >2MB text blobs
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		item := BuildIndexedDoc(kbID, relSlash, string(raw))
		if fi, statErr := d.Info(); statErr == nil {
			item.Doc.UpdatedAt = fi.ModTime().UTC()
		}
		docs = append(docs, item)
		return nil
	})
	return docs, err
}

// ReadDoc reads a single document safely inside rootDir.
func ReadDoc(kb domain.KnowledgeBase, relPath string) (domain.KnowledgeDocContent, error) {
	full, cleanRel, err := safeJoin(RootDir(kb), relPath)
	if err != nil {
		return domain.KnowledgeDocContent{}, err
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return domain.KnowledgeDocContent{}, err
	}
	indexed := BuildIndexedDoc(kb.ID, cleanRel, string(raw))
	if fi, err := os.Stat(full); err == nil {
		indexed.Doc.UpdatedAt = fi.ModTime().UTC()
	}
	return domain.KnowledgeDocContent{
		Doc:     indexed.Doc,
		Content: string(raw),
	}, nil
}

// WriteDoc writes a Markdown document inside any non-readonly knowledge base.
func WriteDoc(kb domain.KnowledgeBase, relPath, content string) (storage.IndexedDoc, error) {
	if kb.ReadOnly {
		return storage.IndexedDoc{}, errors.New("当前知识库为只读模式，不允许直接修改文件")
	}
	full, cleanRel, err := safeJoin(RootDir(kb), relPath)
	if err != nil {
		return storage.IndexedDoc{}, err
	}
	ext := strings.ToLower(filepath.Ext(cleanRel))
	if !allowedExts[ext] {
		return storage.IndexedDoc{}, errors.New("仅允许写入 .md / .markdown / .txt 文档")
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return storage.IndexedDoc{}, err
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return storage.IndexedDoc{}, err
	}
	return BuildIndexedDoc(kb.ID, cleanRel, content), nil
}

func BuildIndexedDoc(kbID, relPath, content string) storage.IndexedDoc {
	sum := sha256.Sum256([]byte(content))
	return storage.IndexedDoc{
		Doc: domain.KnowledgeDoc{
			ID:            uuid.NewString(),
			KBID:          kbID,
			RelPath:       relPath,
			Title:         extractTitle(relPath, content),
			ContentSHA256: hex.EncodeToString(sum[:]),
			SizeBytes:     int64(len(content)),
			UpdatedAt:     time.Now().UTC(),
		},
		Body: content,
	}
}

func extractTitle(relPath, content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			t := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			if t != "" {
				return t
			}
		}
	}
	base := filepath.Base(relPath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func safeJoin(rootDir, relPath string) (string, string, error) {
	cleanRel := filepath.ToSlash(filepath.Clean(strings.TrimSpace(relPath)))
	if cleanRel == "" || cleanRel == "." || strings.HasPrefix(cleanRel, "/") || strings.HasPrefix(cleanRel, "..") || strings.Contains(cleanRel, "/../") {
		return "", "", errors.New("非法的文档相对路径")
	}
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return "", "", err
	}
	full := filepath.Join(absRoot, filepath.FromSlash(cleanRel))
	relCheck, err := filepath.Rel(absRoot, full)
	if err != nil || strings.HasPrefix(relCheck, "..") {
		return "", "", errors.New("路径越界访问被拒绝")
	}
	return full, cleanRel, nil
}

func gitAuthEnv(secret string) ([]string, func(), error) {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if strings.TrimSpace(secret) == "" {
		return env, func() {}, nil
	}
	askpassFile, cleanup, err := writeAskPassScript("oauth2", secret)
	if err != nil {
		return nil, func() {}, err
	}
	env = append(env, "GIT_ASKPASS="+askpassFile)
	return env, cleanup, nil
}

func writeAskPassScript(username, secret string) (string, func(), error) {
	f, err := os.CreateTemp("", "lg-git-askpass-*.sh")
	if err != nil {
		return "", nil, err
	}
	escUser := strings.ReplaceAll(username, `'`, `'\''`)
	escSec := strings.ReplaceAll(secret, `'`, `'\''`)
	script := "#!/bin/sh\ncase \"$1\" in\n  *[Uu]sername*) printf '%s\\n' '" + escUser + "' ;;\n  *) printf '%s\\n' '" + escSec + "' ;;\nesac\n"
	if _, err := f.WriteString(script); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	if err := f.Chmod(0o700); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	_ = f.Close()
	return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
}

func runGitEnv(ctx context.Context, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return stdout.String(), nil
}
