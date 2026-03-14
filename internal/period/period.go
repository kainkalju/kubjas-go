// Package period implements a parser and evaluator compatible with Perl's
// Time::Period module. A period expression restricts job execution to
// specific time windows.
//
// Syntax: scale {range ...} [scale {range ...} ...]
//
// Scales: year/yr, month/mo, week/wk, yday/yd, mday/md, wday/wd,
//         hour/hr, minute/min, second/sec
package period

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// InPeriod returns true if t falls within the period expression.
// An empty expression always returns true.
// Returns an error if the expression is syntactically invalid.
func InPeriod(t time.Time, expr string) (bool, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true, nil
	}
	subPeriods := splitSubPeriods(expr)
	for _, sp := range subPeriods {
		ok, err := evalSubPeriod(t, strings.TrimSpace(sp))
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// Validate checks the expression syntax without evaluating.
// Returns nil if valid.
func Validate(expr string) error {
	_, err := InPeriod(time.Now(), expr)
	return err
}

// splitSubPeriods splits on commas that are outside braces.
func splitSubPeriods(expr string) []string {
	var parts []string
	depth := 0
	start := 0
	for i, ch := range expr {
		switch ch {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, expr[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, expr[start:])
	return parts
}

// evalSubPeriod evaluates a single sub-period (no commas).
// All scale constraints in a sub-period must be satisfied (AND logic).
func evalSubPeriod(t time.Time, expr string) (bool, error) {
	tokens, err := tokenize(expr)
	if err != nil {
		return false, err
	}
	i := 0
	for i < len(tokens) {
		scale := tokens[i]
		i++
		if i >= len(tokens) || tokens[i] != "{" {
			return false, fmt.Errorf("expected '{' after scale %q", scale)
		}
		i++ // consume '{'
		var ranges []string
		for i < len(tokens) && tokens[i] != "}" {
			ranges = append(ranges, tokens[i])
			i++
		}
		if i >= len(tokens) {
			return false, fmt.Errorf("missing '}' in period expression")
		}
		i++ // consume '}'

		val, err := scaleValue(t, scale)
		if err != nil {
			return false, err
		}
		matched, err := matchRanges(val, ranges, scale)
		if err != nil {
			return false, err
		}
		if !matched {
			return false, nil
		}
	}
	return true, nil
}

func tokenize(expr string) ([]string, error) {
	var tokens []string
	i := 0
	runes := []rune(expr)
	for i < len(runes) {
		if unicode.IsSpace(runes[i]) {
			i++
			continue
		}
		if runes[i] == '{' || runes[i] == '}' {
			tokens = append(tokens, string(runes[i]))
			i++
			continue
		}
		// Collect word/number token
		start := i
		for i < len(runes) && !unicode.IsSpace(runes[i]) && runes[i] != '{' && runes[i] != '}' {
			i++
		}
		tokens = append(tokens, string(runes[start:i]))
	}
	return tokens, nil
}

// scaleValue returns the integer value of t for the given scale.
func scaleValue(t time.Time, scale string) (int, error) {
	switch strings.ToLower(scale) {
	case "year", "yr":
		y := t.Year()
		if y >= 1970 {
			return y, nil
		}
		return y % 100, nil
	case "month", "mo":
		return int(t.Month()), nil
	case "week", "wk":
		_, week := t.ISOWeek()
		return week, nil
	case "yday", "yd":
		return t.YearDay(), nil
	case "mday", "md":
		return t.Day(), nil
	case "wday", "wd":
		// Sunday=1 .. Saturday=7 (Perl Time::Period convention)
		return int(t.Weekday()) + 1, nil
	case "hour", "hr":
		return t.Hour(), nil
	case "minute", "min":
		return t.Minute(), nil
	case "second", "sec":
		return t.Second(), nil
	default:
		return 0, fmt.Errorf("unknown scale: %q", scale)
	}
}

// matchRanges returns true if val matches any of the range tokens.
func matchRanges(val int, ranges []string, scale string) (bool, error) {
	for _, r := range ranges {
		lo, hi, err := parseRange(r, scale)
		if err != nil {
			return false, err
		}
		if val >= lo && val <= hi {
			return true, nil
		}
	}
	return false, nil
}

// parseRange parses a single range token like "1", "1-5", "mon", "9am", "12noon".
// Returns [lo, hi] inclusive.
func parseRange(token, scale string) (int, int, error) {
	// Try "N-M" numeric range first
	if idx := strings.Index(token, "-"); idx > 0 {
		loStr := token[:idx]
		hiStr := token[idx+1:]
		lo, err1 := parseValue(loStr, scale)
		hi, err2 := parseValue(hiStr, scale)
		if err1 == nil && err2 == nil {
			return lo, hi, nil
		}
	}
	// Single value
	v, err := parseValue(token, scale)
	if err != nil {
		return 0, 0, err
	}
	return v, v, nil
}

// Month name map
var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4,
	"may": 5, "jun": 6, "jul": 7, "aug": 8,
	"sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

// Weekday name map (Sunday=1)
var wdayNames = map[string]int{
	"su": 1, "sun": 1, "sunday": 1,
	"mo": 2, "mon": 2, "monday": 2,
	"tu": 3, "tue": 3, "tuesday": 3,
	"we": 4, "wed": 4, "wednesday": 4,
	"th": 5, "thu": 5, "thursday": 5,
	"fr": 6, "fri": 6, "friday": 6,
	"sa": 7, "sat": 7, "saturday": 7,
}

func parseValue(s, scale string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))

	// Numeric
	if n, err := strconv.Atoi(s); err == nil {
		return n, nil
	}

	sc := strings.ToLower(scale)

	// Hour special cases: 12am=0, Xam, 12noon=12, 12pm=12, Xpm
	if sc == "hour" || sc == "hr" {
		if s == "12am" {
			return 0, nil
		}
		if s == "12noon" || s == "12pm" {
			return 12, nil
		}
		if strings.HasSuffix(s, "am") {
			n, err := strconv.Atoi(strings.TrimSuffix(s, "am"))
			if err != nil {
				return 0, fmt.Errorf("invalid hour value %q", s)
			}
			if n == 12 {
				return 0, nil
			}
			return n, nil
		}
		if strings.HasSuffix(s, "pm") {
			n, err := strconv.Atoi(strings.TrimSuffix(s, "pm"))
			if err != nil {
				return 0, fmt.Errorf("invalid hour value %q", s)
			}
			if n == 12 {
				return 12, nil
			}
			return n + 12, nil
		}
	}

	// Month names
	if sc == "month" || sc == "mo" {
		if v, ok := monthNames[s]; ok {
			return v, nil
		}
	}

	// Weekday names
	if sc == "wday" || sc == "wd" {
		if v, ok := wdayNames[s]; ok {
			return v, nil
		}
	}

	return 0, fmt.Errorf("cannot parse value %q for scale %q", s, scale)
}
