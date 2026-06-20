package clients

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// StdinPrompter asks the user questions on a terminal, reading single-line
// answers from an input stream.
type StdinPrompter struct {
	out     io.Writer
	scanner *bufio.Scanner
}

// NewStdinPrompter constructs a StdinPrompter that prints prompts to out and
// reads answers from in (typically os.Stdout and os.Stdin).
func NewStdinPrompter(out io.Writer, in io.Reader) *StdinPrompter {
	return &StdinPrompter{out: out, scanner: bufio.NewScanner(in)}
}

// Ask prints the question and returns the user's typed answer. It returns
// io.EOF if the input stream closes before an answer is given (e.g. Ctrl+D).
func (p *StdinPrompter) Ask(question string) (string, error) {
	fmt.Fprintf(p.out, "\n%s\n> ", question)
	if !p.scanner.Scan() {
		if err := p.scanner.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return strings.TrimSpace(p.scanner.Text()), nil
}
