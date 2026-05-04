package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/steipete/wacli/internal/resolve"
	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

type recipientOptions struct {
	pick   int
	asJSON bool
}

type recipientResolverApp interface {
	DB() *store.DB
}

func resolveRecipient(a recipientResolverApp, input string, opts recipientOptions) (types.JID, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return types.JID{}, fmt.Errorf("--to is required")
	}
	if opts.pick < 0 {
		return types.JID{}, fmt.Errorf("--pick must be a positive integer, got %d", opts.pick)
	}
	if strings.Contains(input, "@") {
		return wa.ParseUserOrJID(input)
	}

	phoneShaped := resolve.LooksLikePhone(input)
	candidates, err := resolve.Resolve(a.DB(), input, 10)
	if err != nil {
		return types.JID{}, err
	}
	if phoneShaped {
		candidates = exactPhoneCandidates(candidates)
	}

	if len(candidates) == 0 {
		if phoneShaped {
			return wa.ParseUserOrJID(resolve.NormalizePhone(input))
		}
		return types.JID{}, fmt.Errorf("no contacts, groups, or chats match %q (try `wacli contacts search` or pass a JID)", input)
	}

	if opts.pick > 0 {
		if opts.pick > len(candidates) {
			return types.JID{}, fmt.Errorf("--pick %d is out of range (only %d match%s for %q)", opts.pick, len(candidates), plural(len(candidates)), input)
		}
		return parseCandidateJID(candidates[opts.pick-1])
	}
	if len(candidates) == 1 {
		return parseCandidateJID(candidates[0])
	}
	if opts.asJSON || !isInteractive() {
		return types.JID{}, ambiguousRecipientError(input, candidates)
	}

	pick, err := promptCandidate(os.Stderr, os.Stdin, input, candidates)
	if err != nil {
		return types.JID{}, err
	}
	return parseCandidateJID(candidates[pick])
}

func exactPhoneCandidates(candidates []resolve.Candidate) []resolve.Candidate {
	exact := candidates[:0:0]
	for _, c := range candidates {
		if c.Score < resolve.ScoreExact {
			continue
		}
		if c.Kind == resolve.KindChat && !strings.HasSuffix(c.JID, "@g.us") {
			continue
		}
		exact = append(exact, c)
	}
	return exact
}

func parseCandidateJID(c resolve.Candidate) (types.JID, error) {
	jid, err := wa.ParseUserOrJID(c.JID)
	if err != nil {
		return types.JID{}, fmt.Errorf("parse resolved JID %q: %w", c.JID, err)
	}
	return jid, nil
}

func ambiguousRecipientError(input string, candidates []resolve.Candidate) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%q matches %d recipients; pass a JID or use --pick N:\n", input, len(candidates))
	for i, c := range candidates {
		fmt.Fprintf(&b, "  %d) %s\n", i+1, formatCandidate(c))
	}
	return fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
}

func formatCandidate(c resolve.Candidate) string {
	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = c.JID
	}
	detail := strings.TrimSpace(c.Detail)
	kind := string(c.Kind)
	if detail != "" && detail != kind {
		return fmt.Sprintf("%-30s  %-16s  [%s]  %s", name, detail, kind, c.JID)
	}
	return fmt.Sprintf("%-30s  %-16s  [%s]  %s", name, "", kind, c.JID)
}

func promptCandidate(w io.Writer, r io.Reader, input string, candidates []resolve.Candidate) (int, error) {
	fmt.Fprintf(w, "%q matches %d recipients:\n", input, len(candidates))
	for i, c := range candidates {
		fmt.Fprintf(w, "  %d) %s\n", i+1, formatCandidate(c))
	}
	fmt.Fprintf(w, "Pick [1-%d] (or q to cancel): ", len(candidates))

	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		return 0, fmt.Errorf("no selection made")
	}
	line := strings.TrimSpace(scanner.Text())
	if line == "" || strings.EqualFold(line, "q") {
		return 0, fmt.Errorf("cancelled")
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(candidates) {
		return 0, fmt.Errorf("invalid selection %q", line)
	}
	return n - 1, nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "es"
}
