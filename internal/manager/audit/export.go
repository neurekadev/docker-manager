package audit

import "strings"

// SafeCSVCell neutralizes spreadsheet formula injection (CWE-1236): a
// cell starting with =, +, -, @, tab or carriage return is prefixed with a
// single quote so spreadsheet applications treat it as text. Values are
// otherwise unchanged; encoding/csv handles quoting.
func SafeCSVCell(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r', '\n':
		return "'" + s
	}
	// Formulas may also hide behind leading spaces in some applications.
	if t := strings.TrimLeft(s, " "); t != s && t != "" && strings.ContainsRune("=+-@", rune(t[0])) {
		return "'" + s
	}
	return s
}
