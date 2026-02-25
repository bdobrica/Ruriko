package nlp

import (
	"fmt"
	"strconv"
	"strings"
)

// CronFlagKeys is the set of command flag names that may contain a standard
// 5-field cron expression.  The Classifier scans these keys in every
// ClassifyResponse (both top-level flags and per-step flags for plans) to
// validate any cron expression the LLM produces before returning the response
// to the caller.
//
// Keep this set in sync with any flag names the command catalogue advertises
// for schedule-related operations.
var CronFlagKeys = map[string]struct{}{
	"cron":      {},
	"schedule":  {},
	"cron-expr": {},
}

// ValidateCronExpression reports whether expr is a syntactically valid
// standard Unix cron expression with exactly 5 whitespace-separated fields:
//
//	<minute> <hour> <day-of-month> <month> <day-of-week>
//
// Each field may contain:
//   - A wildcard:        *
//   - A plain integer:   5
//   - A range:           1-5
//   - A step:            */15  or  1-5/2
//   - A list:            1,3,5  or  */15,30
//
// The validator enforces value bounds for each field:
//
//	minute       0–59
//	hour         0–23
//	day-of-month 1–31
//	month        1–12
//	day-of-week  0–7  (0 and 7 are both Sunday)
//
// Returns nil when the expression is valid; a descriptive error otherwise.
// Calendar semantics (e.g. "Feb 30") are not enforced — the check is purely
// syntactic.
func ValidateCronExpression(expr string) error {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return fmt.Errorf(
			"cron expression must have exactly 5 fields (minute hour dom month dow), got %d in %q",
			len(fields), expr,
		)
	}

	type fieldSpec struct {
		name string
		min  int
		max  int
	}
	specs := [5]fieldSpec{
		{"minute", 0, 59},
		{"hour", 0, 23},
		{"day-of-month", 1, 31},
		{"month", 1, 12},
		{"day-of-week", 0, 7},
	}

	for i, f := range specs {
		if err := validateCronField(fields[i], f.min, f.max, f.name); err != nil {
			return err
		}
	}
	return nil
}

// validateCronField validates a single cron field, which may be a
// comma-separated list of individual items (e.g. "1,3,5" or "*/15,30").
func validateCronField(field string, min, max int, name string) error {
	for _, part := range strings.Split(field, ",") {
		if err := validateCronItem(part, min, max, name); err != nil {
			return err
		}
	}
	return nil
}

// validateCronItem validates one non-comma token:
//
//	"*"      – unconditional wildcard
//	"*/n"    – every-n step over the full range
//	"n"      – absolute integer
//	"n-m"    – range
//	"n-m/s"  – range with step
func validateCronItem(item string, min, max int, name string) error {
	// Split off any trailing "/step" suffix.
	parts := strings.SplitN(item, "/", 2)
	base := parts[0]

	if len(parts) == 2 {
		step, err := strconv.Atoi(parts[1])
		if err != nil || step < 1 {
			return fmt.Errorf("cron field %q: invalid step %q in item %q", name, parts[1], item)
		}
	}

	// Wildcard — valid for any field.
	if base == "*" {
		return nil
	}

	// Range "n-m".
	if idx := strings.Index(base, "-"); idx != -1 {
		lo, err1 := strconv.Atoi(base[:idx])
		hi, err2 := strconv.Atoi(base[idx+1:])
		if err1 != nil || err2 != nil {
			return fmt.Errorf("cron field %q: invalid range %q", name, base)
		}
		if lo < min || hi > max || lo > hi {
			return fmt.Errorf(
				"cron field %q: range %d-%d is out of bounds [%d,%d] or inverted",
				name, lo, hi, min, max,
			)
		}
		return nil
	}

	// Plain integer.
	n, err := strconv.Atoi(base)
	if err != nil {
		return fmt.Errorf("cron field %q: unrecognised token %q", name, base)
	}
	// day-of-week: allow 7 as an alias for Sunday (same as 0).
	if name == "day-of-week" && n == 7 {
		return nil
	}
	if n < min || n > max {
		return fmt.Errorf(
			"cron field %q: value %d is out of bounds [%d,%d]",
			name, n, min, max,
		)
	}
	return nil
}

// validateResponseCronFlags inspects all cron-related flags in resp (both
// top-level flags and per-step flags for plan responses) and returns the first
// validation error encountered.
//
// The caller (Classifier.Classify) uses this to reject responses that contain
// a syntactically invalid cron expression before handing the response back to
// the NL dispatch layer.
func validateResponseCronFlags(resp *ClassifyResponse) error {
	if err := validateFlagMapCron(resp.Flags); err != nil {
		return err
	}
	for _, step := range resp.Steps {
		if err := validateFlagMapCron(step.Flags); err != nil {
			return err
		}
	}
	return nil
}

// validateFlagMapCron iterates over flags and validates the value of any key
// that appears in CronFlagKeys.
func validateFlagMapCron(flags map[string]string) error {
	for key, val := range flags {
		if _, ok := CronFlagKeys[key]; ok {
			if err := ValidateCronExpression(val); err != nil {
				return fmt.Errorf("flag %q contains an invalid cron expression: %w", key, err)
			}
		}
	}
	return nil
}
