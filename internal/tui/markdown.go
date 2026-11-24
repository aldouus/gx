package tui

import "strings"

func assistantRows(s string, cols int) []string {
	if s == "" {
		return []string{""}
	}
	lines := strings.Split(s, "\n")
	var out []string
	inFence := false
	var openMark byte
	var openRun int
	for i := 0; i < len(lines); {
		if mark, run, ok := fenceAt(lines[i]); ok {
			if !inFence {
				inFence = true
				openMark, openRun = mark, run
			} else if mark == openMark && run >= openRun && fenceTailBlank(lines[i], run) {
				inFence = false
			}
			if strings.TrimSpace(lines[i]) != "" {
				out = append(out, wrapPlain(indentText(linkPaths(lines[i])), cols)...)
			}
			i++
			continue
		}
		if !inFence {
			if n := tableSpan(lines[i:]); n >= 2 {
				out = append(out, renderTable(lines[i:i+n], cols)...)
				i += n
				continue
			}
		}
		if strings.TrimSpace(lines[i]) == "" {
			i++
			continue
		}
		out = append(out, wrapPlain(indentText(linkPaths(lines[i])), cols)...)
		i++
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// committable returns a prefix of live that can leave the frame. A pipe block
// stays together until a later line shows whether it is a table. Only a header
// plus a separator row is a table. Anything else is ordinary text, so a single
// pipe in prose is not held back.
func committable(live string) (block, rest string, ok bool) {
	if strings.Trim(live, "\n") == "" {
		return "", "", false
	}
	complete, partial, hasPartial := splitLive(live)
	if len(complete) == 0 {
		return "", "", false
	}
	if _, _, isFence := fenceAt(complete[0]); isFence {
		n, closed := fenceBlock(complete)
		if !closed {
			return "", "", false
		}
		return strings.Join(complete[:n], "\n"), joinRest(complete[n:], partial, hasPartial), true
	}
	if isPipeLine(complete[0]) {
		n := 0
		for n < len(complete) && isPipeLine(complete[n]) {
			n++
		}
		closed := n < len(complete) || (hasPartial && !extendsPipe(partial))
		if !closed {
			return "", "", false
		}
		joined := strings.Join(complete[:n], "\n")
		if isValidTable(joined) {
			return joined, joinRest(complete[n:], partial, hasPartial), true
		}
		return complete[0], joinRest(complete[1:], partial, hasPartial), true
	}
	rest = joinRest(complete[1:], partial, hasPartial)
	if strings.Trim(rest, "\n") == "" {
		return "", "", false
	}
	return complete[0], rest, true
}

func splitLive(live string) (complete []string, partial string, hasPartial bool) {
	ends := strings.HasSuffix(live, "\n")
	parts := strings.Split(live, "\n")
	if ends {
		if len(parts) > 0 {
			parts = parts[:len(parts)-1]
		}
		return parts, "", false
	}
	if len(parts) == 0 {
		return nil, "", false
	}
	return parts[:len(parts)-1], parts[len(parts)-1], true
}

func joinRest(lines []string, partial string, hasPartial bool) string {
	if hasPartial {
		lines = append(append([]string{}, lines...), partial)
	}
	return strings.Join(lines, "\n")
}

func fenceAt(line string) (marker byte, run int, ok bool) {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i >= len(line) {
		return 0, 0, false
	}
	marker = line[i]
	if marker != '`' && marker != '~' {
		return 0, 0, false
	}
	start := i
	for i < len(line) && line[i] == marker {
		i++
	}
	run = i - start
	if run < 3 {
		return 0, 0, false
	}
	return marker, run, true
}

func fenceTailBlank(line string, run int) bool {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	i += run
	for i < len(line) {
		if line[i] != ' ' && line[i] != '\t' {
			return false
		}
		i++
	}
	return true
}

func fenceBlock(complete []string) (n int, closed bool) {
	mark, run, ok := fenceAt(complete[0])
	if !ok {
		return 0, false
	}
	for i := 1; i < len(complete); i++ {
		m, r, fence := fenceAt(complete[i])
		if fence && m == mark && r >= run && fenceTailBlank(complete[i], r) {
			return i + 1, true
		}
	}
	return 0, false
}

func tableSpan(lines []string) int {
	if len(lines) < 2 || !isPipeLine(lines[0]) || !isSeparatorLine(lines[1]) {
		return 0
	}
	n := 2
	for n < len(lines) && isPipeLine(lines[n]) && !isSeparatorLine(lines[n]) {
		n++
	}
	return n
}

func isPipeLine(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] == '\\' && i+1 < len(trimmed) {
			i++
			continue
		}
		if trimmed[i] == '|' {
			return true
		}
	}
	return false
}

func isSeparatorLine(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return false
	}
	dash, pipe := false, false
	for i := 0; i < len(trimmed); i++ {
		switch trimmed[i] {
		case '|':
			pipe = true
		case ':', ' ', '\t':
		case '-':
			dash = true
		default:
			return false
		}
	}
	return dash && pipe
}

func isValidTable(buf string) bool {
	lines := strings.Split(buf, "\n")
	return len(lines) >= 2 && isSeparatorLine(lines[1])
}

func extendsPipe(s string) bool {
	if s == "" || isPipeLine(s) {
		return true
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '|', ':', ' ', '\t', '-':
		default:
			return false
		}
	}
	return true
}
