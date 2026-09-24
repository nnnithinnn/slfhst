// Package prompt is the interactive console-input layer used by the
// installer's wizard/netconf stage1 steps -- direct port of common.py's
// ask()/banner() helpers.
package prompt

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// stdin is a var (not a local in Ask) so tests can substitute a
// strings.Reader instead of the real console.
var stdin = bufio.NewReader(os.Stdin)

// Options configures a single Ask call.
type Options struct {
	Default  string
	Validate func(string) bool
	// Normalize, if set, runs on the trimmed input before Validate and
	// before the value is returned -- e.g. stripping embedded whitespace
	// from a pasted IP address.
	Normalize func(string) string
	// Optional inverts common.py's ask() default of required=True --
	// Go's zero value (false) naturally matches "required" as the
	// default, so this project's usual "explicit non-default states its
	// name" pattern holds without an extra bool-meaning inversion at
	// call sites.
	Optional bool
}

// Ask prompts label on the console, re-prompting until a valid answer
// (or, if Optional, a blank one) is given.
func Ask(label string, opts Options) (string, error) {
	display := label
	if opts.Default != "" {
		display = fmt.Sprintf("%s [%s]", label, opts.Default)
	}
	for {
		fmt.Printf("%s: ", display)
		line, err := stdin.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("prompt: read input: %w", err)
		}
		value := strings.TrimSpace(line)
		if value == "" {
			value = opts.Default
		}
		if opts.Normalize != nil && value != "" {
			value = opts.Normalize(value)
		}
		if value == "" {
			if opts.Optional {
				return "", nil
			}
			fmt.Println("a value is required")
			continue
		}
		if opts.Validate != nil && !opts.Validate(value) {
			fmt.Println("invalid value, try again")
			continue
		}
		return value, nil
	}
}

// Banner prints a bordered banner, matching common.py's banner().
func Banner(text string) {
	line := strings.Repeat("=", 70)
	fmt.Println(line)
	fmt.Println(text)
	fmt.Println(line)
}
