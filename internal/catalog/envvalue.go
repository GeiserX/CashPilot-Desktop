package catalog

import (
	"regexp"
	"strconv"
	"strings"
)

// AcceptsValue reports whether a value the user supplied for this field has the
// format the deploy needs: all of the pattern, and a well-ordered range for a port
// range field. An empty value is always accepted here; whether it may be empty is
// the Required/Default question, asked separately.
//
// It is the one rule the deploy validation (services.validateRequired) and the
// import dry run (internal/importers) both apply, so a value the dry run shows as
// ready is a value the deploy takes.
func (v EnvVar) AcceptsValue(value string) bool {
	if value == "" {
		return true
	}
	if v.Pattern != "" {
		re, err := regexp.Compile(`^(?:` + v.Pattern + `)$`)
		if err != nil || !re.MatchString(value) {
			return false
		}
	}
	if v.PortRange && !IsPortRange(value) {
		return false
	}
	return true
}

// IsPortRange reports whether value is "start:end" in ASCII digits with
// 1 <= start <= end <= 65535.
func IsPortRange(value string) bool {
	start, end, ok := strings.Cut(value, ":")
	if !ok {
		return false
	}
	lo, err1 := strconv.Atoi(start)
	hi, err2 := strconv.Atoi(end)
	digits := func(s string) bool { return s != "" && strings.Trim(s, "0123456789") == "" }
	return err1 == nil && err2 == nil && digits(start) && digits(end) && 1 <= lo && lo <= hi && hi <= 65535
}
