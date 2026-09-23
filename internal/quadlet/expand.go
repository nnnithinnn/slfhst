package quadlet

import (
	"fmt"
	"regexp"
	"strings"
)

// varPattern matches Python string.Template's default syntax:
// ${identifier} or $identifier.
var varPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// strictExpand substitutes ${VAR}/$VAR references in text from vars,
// matching string.Template.substitute()'s strict behavior: any
// referenced variable missing from vars is an error, not a silent
// pass-through (unlike .safe_substitute(), which this project's Python
// code deliberately didn't use either).
func strictExpand(text string, vars map[string]string) (string, error) {
	var missing []string
	result := varPattern.ReplaceAllStringFunc(text, func(match string) string {
		sub := varPattern.FindStringSubmatch(match)
		name := sub[1]
		if name == "" {
			name = sub[2]
		}
		v, ok := vars[name]
		if !ok {
			missing = append(missing, name)
			return match
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("undefined template variable(s): %s", strings.Join(missing, ", "))
	}
	return result, nil
}
