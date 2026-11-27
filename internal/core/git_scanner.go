package core

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// GitCommit holds commit metadata inspected from a local Git repository.
type GitCommit struct {
	SHA         string
	Title       string
	Author      string
	AuthorEmail string
	CommittedAt string
	PatchID     string
}

// InspectBranchCommits reads up to limit commits from branch in repoPath.
// When authorAliases is non-empty, only commits whose author name or email
// matches one of the aliases (case-insensitive) are returned.
func InspectBranchCommits(ctx context.Context, repoPath, branch string, authorAliases []string, limit int) ([]GitCommit, error) {
	if limit <= 0 {
		limit = 50
	}
	format := "%H%x1f%s%x1f%an%x1f%ae%x1f%aI%x1e"
	args := []string{"-C", repoPath, "log", fmt.Sprintf("-n%d", limit), "--format=" + format}
	if strings.TrimSpace(branch) != "" {
		args = append(args, branch)
	}
	out, err := runGit(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("git log (%s @ %s): %w", repoPath, branch, err)
	}

	aliasSet := make([]string, 0, len(authorAliases))
	for _, a := range authorAliases {
		if trimmed := strings.ToLower(strings.TrimSpace(a)); trimmed != "" {
			aliasSet = append(aliasSet, trimmed)
		}
	}

	records := strings.Split(out, "\x1e")
	commits := make([]GitCommit, 0, len(records))
	for _, rec := range records {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		parts := strings.Split(rec, "\x1f")
		if len(parts) < 5 {
			continue
		}
		c := GitCommit{
			SHA:         parts[0],
			Title:       parts[1],
			Author:      parts[2],
			AuthorEmail: parts[3],
			CommittedAt: parts[4],
		}
		if len(aliasSet) > 0 && !matchesAuthor(c.Author, c.AuthorEmail, aliasSet) {
			continue
		}
		patchID, err := ComputePatchID(ctx, repoPath, c.SHA)
		if err == nil {
			c.PatchID = patchID
		}
		commits = append(commits, c)
	}
	return commits, nil
}

// ComputePatchID computes Git's stable patch-id for a commit so rebased copies
// of an identical patch can be paired reliably even when commit SHAs change.
func ComputePatchID(ctx context.Context, repoPath, sha string) (string, error) {
	diff, err := runGit(ctx, "-C", repoPath, "show", "--format=", "--patch", "--no-color", sha)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(diff) == "" {
		return "", nil
	}
	cmd := exec.CommandContext(ctx, "git", "patch-id", "--stable")
	cmd.Stdin = strings.NewReader(diff)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git patch-id: %s", strings.TrimSpace(stderr.String()))
	}
	fields := strings.Fields(stdout.String())
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

func matchesAuthor(name, email string, aliases []string) bool {
	ln := strings.ToLower(strings.TrimSpace(name))
	le := strings.ToLower(strings.TrimSpace(email))
	for _, alias := range aliases {
		if ln == alias || le == alias || strings.Contains(le, alias) {
			return true
		}
	}
	return false
}

func runGit(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
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
