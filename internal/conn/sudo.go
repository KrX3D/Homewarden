package conn

import "fmt"

// Sudo returns cmd prefixed with `sudo -n`. The prefix is kept (no `sh -c` wrapper)
// so a sudoers rule that only allows the package manager is enough. That only works
// for one simple command: in `a && b`, `a | b`, `a; b` or `x $(a)` the shell runs
// everything but the first command as the login user. Sudo rejects such lines
// instead of running them half privileged. Redirections (`2>/dev/null`, `2>&1`) and
// operators inside quotes are fine.
func Sudo(cmd string) (string, error) {
	if op := compoundOperator(cmd); op != "" {
		return "", fmt.Errorf("a command run with sudo must be a single command, but this one contains %q, so the part after it would not run as root; split it into separate commands", op)
	}
	return "sudo -n " + cmd, nil
}

// compoundOperator returns the first shell operator outside single quotes that makes
// cmd more than one simple command, or "" when there is none. Inside double quotes
// only command substitution counts, since the shell still runs it as the login user.
func compoundOperator(cmd string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		next := byte(0)
		if i+1 < len(cmd) {
			next = cmd[i+1]
		}
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
			continue
		case c == '\\':
			i++ // escaped character, also inside double quotes
			continue
		case c == '`':
			return "`"
		case c == '$' && next == '(':
			return "$("
		case inDouble:
			if c == '"' {
				inDouble = false
			}
			continue
		}
		switch c {
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case '\n':
			return "newline"
		case ';':
			return ";"
		case '|':
			if i > 0 && cmd[i-1] == '>' {
				continue // >| (clobber redirection)
			}
			if next == '|' {
				return "||"
			}
			return "|"
		case '&':
			if i > 0 && (cmd[i-1] == '>' || cmd[i-1] == '<') || next == '>' {
				continue // 2>&1, <&3, &>file: redirections, not a second command
			}
			if next == '&' {
				return "&&"
			}
			return "&"
		}
	}
	return ""
}
