package diff

import (
	"slices"
	"sort"
	"strings"

	"github.com/suprbdev/pgy/internal/schema"
)

// StoredRoutineBody returns the text PostgreSQL stores (pg_proc.prosrc) for
// a routine declared with the string body body:
//
//   - a body on one line is stored as written;
//   - a body of several lines starts on the line after the opening dollar
//     quote and ends on the line before the closing one: a newline is put in
//     front of it, and one after it unless it already ends with one;
//   - a body ending in three or more newlines is stored as written, without
//     those three.
//
// The last form reaches the texts the first two cannot, such as a body of
// several lines that starts right after the opening quote.
// DeclaredRoutineBody is the inverse.
func StoredRoutineBody(body string) string {
	trailing := len(body) - len(strings.TrimRight(body, "\n"))
	switch {
	case trailing >= 3:
		return body[:len(body)-3]
	case !strings.Contains(body, "\n"):
		return body
	case trailing == 0:
		return "\n" + body + "\n"
	}
	return "\n" + body
}

// DeclaredRoutineBody returns a body that StoredRoutineBody maps back to
// src, a routine's stored text, byte for byte. A text on lines of its own
// between the dollar quotes becomes those lines; other texts of several
// lines get three newlines appended.
func DeclaredRoutineBody(src string) string {
	if !strings.Contains(src, "\n") {
		return src
	}
	trailing := len(src) - len(strings.TrimRight(src, "\n"))
	if strings.HasPrefix(src, "\n") && len(src) > 1 && trailing >= 1 && trailing <= 2 {
		return src[1:]
	}
	return src + "\n\n\n"
}

// CompiledLanguage reports whether language is one whose functions pgy
// cannot express: their body names an object file or a built-in symbol.
func CompiledLanguage(language string) bool {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "c", "internal":
		return true
	}
	return false
}

// routineLanguageChanged reports whether a routine declared in language is
// written in another one in the database. An undeclared or unknown language
// is not compared. PostgreSQL accepts the name as an identifier or, for
// backward compatibility, as a string literal.
func routineLanguageChanged(language, live string) bool {
	name, _ := schema.SplitLanguage(language)
	name = strings.Trim(name, `"'`)
	return name != "" && live != "" && !strings.EqualFold(name, live)
}

// transformChanged reports whether the declared transform types differ from
// the live ones, in any order. nil declared types are unmanaged.
func transformChanged(declared, live []string) bool {
	if declared == nil {
		return false
	}
	return !slices.Equal(normalizedTypeSet(declared), normalizedTypeSet(live))
}

func normalizedTypeSet(types []string) []string {
	out := make([]string, 0, len(types))
	for _, t := range types {
		out = append(out, normalizeColumnType(t))
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// routineLanguageSQL renders the LANGUAGE clause of a routine, with its
// TRANSFORM FOR TYPE list. A routine that declares no transforms, neither
// as a list nor in its language, keeps the live ones, which CREATE OR
// REPLACE would otherwise remove.
func routineLanguageSQL(language string, transform, live []string) string {
	name, inLanguage := schema.SplitLanguage(language)
	switch {
	case transform != nil:
	case inLanguage != nil:
		return language
	default:
		transform = live
	}
	if len(transform) == 0 {
		return name
	}
	parts := make([]string, len(transform))
	for i, t := range transform {
		parts[i] = "for type " + t
	}
	return name + " transform " + strings.Join(parts, ", ")
}

// routineTransforms maps the normalized signature of each live function and
// procedure that has transforms to their types.
func routineTransforms(live *Live) map[string][]string {
	out := map[string][]string{}
	if live == nil {
		return out
	}
	for norm, lf := range live.FunctionDefs {
		if lf != nil && len(lf.Transform) > 0 {
			out[norm] = lf.Transform
		}
	}
	for norm, lp := range live.ProcedureDefs {
		if lp != nil && len(lp.Transform) > 0 {
			out[norm] = lp.Transform
		}
	}
	return out
}
