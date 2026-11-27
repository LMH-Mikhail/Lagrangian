package knowledge

import (
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/lagrangian-dev/lagrangian/internal/domain"
	"github.com/lagrangian-dev/lagrangian/internal/storage"
)

var (
	wikiLinkRe = regexp.MustCompile(`\[\[([^\[\]\r\n]+)\]\]`)
	mdLinkRe   = regexp.MustCompile(`\[[^\[\]\r\n]*\]\(([^()\r\n]+)\)`)
	fenceRe    = regexp.MustCompile("(?m)^\\s*```")
	inlineCode = regexp.MustCompile("`[^`\r\n]+`")
)

// BuildGraph extracts Obsidian-style [[wikilinks]] and relative Markdown links
// across all documents in a knowledge base and builds a per-KB relationship graph.
func BuildGraph(kb domain.KnowledgeBase, docs []storage.IndexedDoc) domain.KnowledgeGraph {
	// Lookup indexes for Obsidian-compatible link resolution
	exactPath := make(map[string]string, len(docs))
	noExtPath := make(map[string]string, len(docs))
	byStem := make(map[string][]string, len(docs))

	for _, item := range docs {
		rel := strings.TrimSpace(item.Doc.RelPath)
		if rel == "" {
			continue
		}
		low := strings.ToLower(rel)
		exactPath[low] = rel
		noExt := strings.ToLower(stripDocExt(rel))
		noExtPath[noExt] = rel
		stem := strings.ToLower(stripDocExt(path.Base(rel)))
		byStem[stem] = append(byStem[stem], rel)
	}

	resolveTarget := func(sourceRel, rawTarget string) string {
		t := strings.TrimSpace(rawTarget)
		if t == "" {
			return ""
		}
		// Strip escaped or plain alias: [[target|alias]] or [[target\|alias]]
		if idx := strings.Index(t, `\|`); idx >= 0 {
			t = t[:idx]
		} else if idx := strings.Index(t, "|"); idx >= 0 {
			t = t[:idx]
		}
		// Strip heading / block anchor: target#section
		if idx := strings.Index(t, "#"); idx >= 0 {
			t = t[:idx]
		}
		t = strings.TrimSpace(t)
		t = strings.TrimPrefix(t, "./")
		t = strings.TrimPrefix(t, "/")
		if t == "" {
			return ""
		}
		low := strings.ToLower(t)
		lowNoExt := strings.ToLower(stripDocExt(t))

		if hit, ok := exactPath[low]; ok {
			return hit
		}
		if hit, ok := noExtPath[lowNoExt]; ok {
			return hit
		}

		// Try resolving relative to source document's folder
		srcDir := path.Dir(sourceRel)
		if srcDir != "." && srcDir != "" {
			joined := path.Clean(srcDir + "/" + t)
			if hit, ok := exactPath[strings.ToLower(joined)]; ok {
				return hit
			}
			if hit, ok := noExtPath[strings.ToLower(stripDocExt(joined))]; ok {
				return hit
			}
		}

		// Stem match (Obsidian shortest-path wikilink semantics)
		stem := strings.ToLower(stripDocExt(path.Base(t)))
		if candidates := byStem[stem]; len(candidates) > 0 {
			if len(candidates) == 1 {
				return candidates[0]
			}
			// Prefer candidate in the same top-level folder, else shortest path
			srcGroup := topGroup(sourceRel)
			best := candidates[0]
			for _, cand := range candidates {
				if topGroup(cand) == srcGroup && topGroup(best) != srcGroup {
					best = cand
					continue
				}
				if topGroup(cand) == topGroup(best) && len(cand) < len(best) {
					best = cand
				}
			}
			return best
		}
		return ""
	}

	inCount := make(map[string]int, len(docs))
	outCount := make(map[string]int, len(docs))
	seenEdge := make(map[string]bool)
	edges := make([]domain.KnowledgeGraphEdge, 0)

	for _, item := range docs {
		src := item.Doc.RelPath
		cleaned := stripCodeBlocks(item.Body)
		rawTargets := extractLinks(cleaned)
		for _, rawT := range rawTargets {
			dst := resolveTarget(src, rawT)
			if dst == "" || dst == src {
				continue
			}
			edgeKey := src + "\x00" + dst
			if seenEdge[edgeKey] {
				continue
			}
			seenEdge[edgeKey] = true
			edges = append(edges, domain.KnowledgeGraphEdge{Source: src, Target: dst})
			outCount[src]++
			inCount[dst]++
		}
	}

	groupSet := map[string]bool{}
	nodes := make([]domain.KnowledgeGraphNode, 0, len(docs))
	for _, item := range docs {
		rel := item.Doc.RelPath
		grp := topGroup(rel)
		groupSet[grp] = true
		inL := inCount[rel]
		outL := outCount[rel]
		nodes = append(nodes, domain.KnowledgeGraphNode{
			ID:       rel,
			Title:    item.Doc.Title,
			Group:    grp,
			Degree:   inL + outL,
			InLinks:  inL,
			OutLinks: outL,
		})
	}

	groups := make([]string, 0, len(groupSet))
	for g := range groupSet {
		groups = append(groups, g)
	}
	sort.Strings(groups)

	return domain.KnowledgeGraph{
		KBID:   kb.ID,
		KBName: kb.Name,
		Nodes:  nodes,
		Edges:  edges,
		Groups: groups,
	}
}

func extractLinks(body string) []string {
	var out []string
	for _, m := range wikiLinkRe.FindAllStringSubmatch(body, -1) {
		if len(m) >= 2 {
			out = append(out, m[1])
		}
	}
	for _, m := range mdLinkRe.FindAllStringSubmatch(body, -1) {
		if len(m) < 2 {
			continue
		}
		target := strings.TrimSpace(m[1])
		// Strip optional markdown link title: (foo.md "title")
		if sp := strings.IndexAny(target, " \t"); sp > 0 {
			target = target[:sp]
		}
		low := strings.ToLower(target)
		if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") ||
			strings.HasPrefix(low, "mailto:") || strings.HasPrefix(low, "#") {
			continue
		}
		out = append(out, target)
	}
	return out
}

func stripCodeBlocks(s string) string {
	lines := strings.Split(s, "\n")
	inFence := false
	var b strings.Builder
	for _, line := range lines {
		if fenceRe.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		b.WriteString(inlineCode.ReplaceAllString(line, ""))
		b.WriteByte('\n')
	}
	return b.String()
}

func stripDocExt(p string) string {
	low := strings.ToLower(p)
	for _, ext := range []string{".markdown", ".md", ".txt"} {
		if strings.HasSuffix(low, ext) {
			return p[:len(p)-len(ext)]
		}
	}
	return p
}

func topGroup(relPath string) string {
	if idx := strings.Index(relPath, "/"); idx > 0 {
		return relPath[:idx]
	}
	return "根目录"
}
