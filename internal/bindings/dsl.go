package bindings

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The text form of bindings and rules (§7.1, §8.1):
//
//	iam.SendEmail :=
//	  render = template.Exec(name: req.template, data: req.data)
//	  send   = smtp.Send(to: req.to, subject: render.subject) [retry: 5, timeout: 10s]
//	  return { message_id: send.id }
//
//	on iam.UserRegistered when event.email != "" :=
//	  send = smtp.Send(to: event.email, text: "hi") [undo: smtp.Recall]
//
// Grammar (a statement ends at a newline outside brackets and strings;
// `//` starts a comment; blank lines are ignored):
//
//	binding := <hook> ":=" { step } [ "return" value ]
//	rule    := "on" <event> [ "when" <cel> ] ":=" { step }
//	step    := <name> "=" <activity> "(" args ")" [ "[" option { "," option } "]" ]
//	args    := ""  |  field { "," field }  |  <cel>
//	value   := "{" [ field { "," field } ] "}"  |  <cel>
//	field   := ( <identifier> | <string> ) ":" <cel>
//	option  := "when" ":" <cel>
//	         | "after" ":" ( <name> | "[" <name> { "," <name> } "]" )
//	         | "undo" ":" <activity> [ "(" args ")" ]
//	         | "retry" ":" <attempts>
//	         | "retry_interval" ":" <duration>  | "retry_max_interval" ":" <duration>
//	         | "retry_backoff" ":" <number>
//	         | "timeout" ":" <duration>         | "heartbeat" ":" <duration>
//
// Lists of fields and options may end with a comma. Durations are Go's
// ("500ms", "1m30s"). An undo without arguments gets the step's output.
// Format writes the canonical text; Parse of it gives the same definition.

// ParseError is where the text does not read; Line and Column count from
// 1 (Column in characters), 0 when the error is not tied to a place.
type ParseError struct {
	Line    int
	Column  int
	Message string
}

func (e ParseError) Error() string {
	if e.Line == 0 {
		return e.Message
	}

	return fmt.Sprintf("%d:%d: %s", e.Line, e.Column, e.Message)
}

// ParseErrors are every error of a text, in order.
type ParseErrors []ParseError

func (es ParseErrors) Error() string {
	msgs := make([]string, 0, len(es))
	for _, e := range es {
		msgs = append(msgs, e.Error())
	}

	return strings.Join(msgs, "; ")
}

// ParseBinding reads the text form of a binding; a text that does not
// read is ParseErrors. Expressions are checked for CEL syntax only:
// names and types are Validate's.
func ParseBinding(text string) (Binding, error) {
	p := newTextParser(text)
	head, body := p.header()

	var b Binding

	if head != nil {
		if strings.HasPrefix(p.src[head.from:head.to], "on ") {
			p.fail(head.from, "a rule where a binding is expected: remove \"on\"")
		} else {
			b.Hook = p.name(*head, "hook")
		}
	}

	returned := false

	for _, st := range body {
		if rest, ok := p.keyword(st, "return"); ok {
			if returned {
				p.fail(st.from, "a second return")
			}

			returned = true
			b.Result = p.result(rest)

			continue
		}

		if returned {
			p.fail(st.from, "a step after return")
		}

		b.Steps = append(b.Steps, p.step(st))
	}

	if len(p.errs) > 0 {
		return Binding{}, p.errs
	}

	return b, nil
}

// ParseRule reads the text form of a rule, as ParseBinding.
func ParseRule(text string) (Rule, error) {
	p := newTextParser(text)
	head, body := p.header()

	var r Rule

	if head != nil {
		rest, ok := p.keyword(*head, "on")
		if !ok {
			p.fail(head.from, "a rule starts with \"on <service>.<Event>\"")
		} else {
			r.Event, r.When = p.ruleHead(rest)
		}
	}

	for _, st := range body {
		if _, ok := p.keyword(st, "return"); ok {
			p.fail(st.from, "a rule has no return")

			continue
		}

		r.Steps = append(r.Steps, p.step(st))
	}

	if len(p.errs) > 0 {
		return Rule{}, p.errs
	}

	return r, nil
}

// span is a range of the source.
type span struct{ from, to int }

type textParser struct {
	text string // as given: positions
	src  string // comments blanked: what is parsed, same offsets
	errs ParseErrors
}

func newTextParser(text string) *textParser {
	return &textParser{text: text, src: blankComments(text)}
}

func (p *textParser) fail(at int, msg string) {
	line, col := p.position(at)
	p.errs = append(p.errs, ParseError{Line: line, Column: col, Message: msg})
}

// position is the line and column (characters) of a byte offset.
func (p *textParser) position(at int) (int, int) {
	at = min(max(at, 0), len(p.text))
	line := 1 + strings.Count(p.text[:at], "\n")
	start := strings.LastIndexByte(p.text[:at], '\n') + 1

	return line, 1 + utf8.RuneCountInString(p.text[start:at])
}

func (p *textParser) str(s span) string { return p.src[s.from:s.to] }

// trim narrows s to its non-space content.
func (p *textParser) trim(s span) span {
	for s.from < s.to && isSpace(p.src[s.from]) {
		s.from++
	}

	for s.to > s.from && isSpace(p.src[s.to-1]) {
		s.to--
	}

	return s
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// header splits the text into the header (before ":=") and the body
// statements.
func (p *textParser) header() (*span, []span) {
	stmts := p.statements()
	if len(stmts) == 0 {
		p.fail(0, "empty definition")

		return nil, nil
	}

	first := stmts[0]

	assignAt := p.find(first, ":=")
	if assignAt < 0 {
		p.fail(first.from, "the first line must end with \":=\"")

		return nil, nil
	}

	head := p.trim(span{first.from, assignAt})
	body := stmts[1:]

	if rest := p.trim(span{assignAt + len(":="), first.to}); rest.from < rest.to {
		body = append([]span{rest}, body...)
	}

	return &head, body
}

// statements are the non-empty statements of the source.
func (p *textParser) statements() []span {
	var out []span

	start, depth := 0, 0

	for i := 0; i < len(p.src); i++ {
		switch c := p.src[i]; c {
		case '"', '\'':
			i = skipString(p.src, i) - 1
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth = max(depth-1, 0)
		case '\n':
			if depth == 0 {
				if s := p.trim(span{start, i}); s.from < s.to {
					out = append(out, s)
				}

				start = i + 1
			}
		}
	}

	if s := p.trim(span{start, len(p.src)}); s.from < s.to {
		out = append(out, s)
	}

	return out
}

// keyword: s starts with the word followed by a space, a bracket or its
// end; rest is what follows.
func (p *textParser) keyword(s span, word string) (span, bool) {
	text := p.str(s)
	if !strings.HasPrefix(text, word) {
		return span{}, false
	}

	if len(text) > len(word) && !isSpace(text[len(word)]) && !strings.ContainsRune("{([", rune(text[len(word)])) {
		return span{}, false
	}

	return p.trim(span{s.from + len(word), s.to}), true
}

func (p *textParser) name(s span, what string) string {
	name := p.str(s)
	if name == "" || strings.ContainsAny(name, " \t\n") {
		p.fail(s.from, fmt.Sprintf("expected a %s name <service>.<Name>, got %q", what, name))

		return ""
	}

	return name
}

// ruleHead is "<event> [when <cel>]".
func (p *textParser) ruleHead(s span) (string, string) {
	text := p.str(s)

	end := strings.IndexAny(text, " \t\n")
	if end < 0 {
		return p.name(s, "event"), ""
	}

	event := p.name(span{s.from, s.from + end}, "event")

	rest, ok := p.keyword(p.trim(span{s.from + end, s.to}), "when")
	if !ok {
		p.fail(s.from+end, "expected \"when <cel>\" or \":=\" after the event")

		return event, ""
	}

	return event, p.expr(rest)
}

// step is "<name> = <activity>(<args>) [<options>]".
func (p *textParser) step(s span) Step {
	var st Step

	text := p.str(s)

	eqAt := strings.IndexByte(text, '=')
	if eqAt < 0 || strings.HasPrefix(text[eqAt:], "==") {
		p.fail(s.from, "expected a step \"<name> = <service>.<Activity>(...)\"")

		return st
	}

	st.Name = strings.TrimSpace(text[:eqAt])
	if !IsIdent(st.Name) {
		p.fail(s.from, fmt.Sprintf("%q is not a step name (an identifier)", st.Name))
	}

	call := p.trim(span{s.from + eqAt + 1, s.to})

	activity, args, rest, ok := p.call(call)
	if !ok {
		return st
	}

	st.Activity = activity
	st.Input = p.args(args)

	rest = p.trim(rest)
	if rest.from == rest.to {
		return st
	}

	if p.src[rest.from] != '[' {
		p.fail(rest.from, "unexpected text after the call: options go in [...]")

		return st
	}

	end := matching(p.src, rest.from)
	if end < 0 || end >= rest.to {
		p.fail(rest.from, "unclosed [")

		return st
	}

	if tail := p.trim(span{end + 1, rest.to}); tail.from < tail.to {
		p.fail(tail.from, "unexpected text after the options")
	}

	p.options(&st, span{rest.from + 1, end})

	return st
}

// call is "<activity>(<args>)" and what follows it.
func (p *textParser) call(s span) (string, span, span, bool) {
	text := p.str(s)

	open := strings.IndexByte(text, '(')
	if open < 0 {
		p.fail(s.from, "expected \"<service>.<Activity>(...)\"")

		return "", span{}, span{}, false
	}

	activity := strings.TrimSpace(text[:open])
	if activity == "" || strings.ContainsAny(activity, " \t\n") {
		p.fail(s.from, fmt.Sprintf("expected an activity name <service>.<Activity>, got %q", activity))
	}

	end := matching(p.src, s.from+open)
	if end < 0 || end >= s.to {
		p.fail(s.from+open, "unclosed (")

		return "", span{}, span{}, false
	}

	return activity, span{s.from + open + 1, end}, span{end + 1, s.to}, true
}

// args is a call's arguments: fields, one expression or nothing.
func (p *textParser) args(s span) Value {
	s = p.trim(s)
	if s.from == s.to {
		return Value{}
	}

	if !p.fieldSyntax(s) {
		return Value{Expr: p.expr(s)}
	}

	return Value{Fields: p.fields(s)}
}

// result is a return's value: "{ fields }" or one expression.
func (p *textParser) result(s span) Value {
	s = p.trim(s)
	if s.from == s.to {
		p.fail(s.from, "return without a value")

		return Value{}
	}

	if p.src[s.from] == '{' && matching(p.src, s.from) == s.to-1 {
		inner := p.trim(span{s.from + 1, s.to - 1})
		if inner.from == inner.to {
			return Value{}
		}

		if p.fieldSyntax(inner) {
			return Value{Fields: p.fields(inner)}
		}
	}

	return Value{Expr: p.expr(s)}
}

// fieldSyntax: s starts with "<identifier or string> :".
func (p *textParser) fieldSyntax(s span) bool {
	_, after, ok := p.key(s)
	if !ok {
		return false
	}

	rest := p.trim(span{after, s.to})

	return rest.from < rest.to && p.src[rest.from] == ':'
}

// key reads a field key at the start of s: an identifier or a quoted
// string; after is the offset past it.
func (p *textParser) key(s span) (string, int, bool) {
	text := p.str(s)
	if text == "" {
		return "", 0, false
	}

	if text[0] == '"' || text[0] == '\'' {
		end := skipString(text, 0)
		if end > len(text) {
			return "", 0, false
		}

		k, err := unquote(text[:end])
		if err != nil {
			return "", 0, false
		}

		return k, s.from + end, true
	}

	n := 0
	for n < len(text) && (text[n] == '_' || isAlnum(text[n])) {
		n++
	}

	if n == 0 || !IsIdent(text[:n]) {
		return "", 0, false
	}

	return text[:n], s.from + n, true
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// fields reads "key: expr, ..." (a trailing comma allowed).
func (p *textParser) fields(s span) []Field {
	var out []Field

	for _, part := range p.split(s, ',') {
		part = p.trim(part)
		if part.from == part.to {
			continue
		}

		k, after, ok := p.key(part)
		if !ok {
			p.fail(part.from, "expected \"<field>: <cel>\"")

			continue
		}

		rest := p.trim(span{after, part.to})
		if rest.from == rest.to || p.src[rest.from] != ':' {
			p.fail(part.from, "expected \"<field>: <cel>\"")

			continue
		}

		out = append(out, Field{Name: k, Expr: p.expr(span{rest.from + 1, rest.to})})
	}

	return out
}

//nolint:cyclop // one case per option
func (p *textParser) options(st *Step, s span) {
	for _, part := range p.split(s, ',') {
		part = p.trim(part)
		if part.from == part.to {
			continue
		}

		text := p.str(part)

		colon := strings.IndexByte(text, ':')
		if colon < 0 {
			p.fail(part.from, "expected \"<option>: <value>\"")

			continue
		}

		opt := strings.TrimSpace(text[:colon])
		value := p.trim(span{part.from + colon + 1, part.to})
		raw := p.str(value)

		switch opt {
		case "when":
			st.When = p.expr(value)
		case "after":
			st.After = p.after(value)
		case "undo":
			st.Undo, st.UndoInput = p.undo(value)
		case "retry":
			n, err := strconv.Atoi(raw)
			if err != nil || n <= 0 {
				p.fail(value.from, fmt.Sprintf("retry: expected a number of attempts, got %q", raw))
			}

			st.Retry.Attempts = n
		case "retry_interval":
			st.Retry.InitialInterval = p.duration(value)
		case "retry_max_interval":
			st.Retry.MaxInterval = p.duration(value)
		case "retry_backoff":
			f, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				p.fail(value.from, fmt.Sprintf("retry_backoff: expected a number, got %q", raw))
			}

			st.Retry.Backoff = f
		case "timeout":
			st.StartToClose = p.duration(value)
		case "heartbeat":
			st.Heartbeat = p.duration(value)
		default:
			p.fail(part.from, fmt.Sprintf("unknown option %q (when, after, undo, retry, retry_interval, "+
				"retry_max_interval, retry_backoff, timeout, heartbeat)", opt))
		}
	}
}

func (p *textParser) duration(s span) time.Duration {
	d, err := time.ParseDuration(p.str(s))
	if err != nil || d <= 0 {
		p.fail(s.from, fmt.Sprintf("expected a positive duration (10s, 1m30s), got %q", p.str(s)))
	}

	return d
}

func (p *textParser) after(s span) []string {
	text := p.str(s)
	if !strings.HasPrefix(text, "[") {
		if !IsIdent(text) {
			p.fail(s.from, fmt.Sprintf("after: expected a step name, got %q", text))
		}

		return []string{text}
	}

	end := matching(p.src, s.from)
	if end != s.to-1 {
		p.fail(s.from, "after: unclosed [")

		return nil
	}

	var out []string

	for _, part := range p.split(span{s.from + 1, end}, ',') {
		part = p.trim(part)
		if part.from == part.to {
			continue
		}

		if name := p.str(part); IsIdent(name) {
			out = append(out, name)
		} else {
			p.fail(part.from, fmt.Sprintf("after: expected a step name, got %q", name))
		}
	}

	return out
}

func (p *textParser) undo(s span) (string, Value) {
	if !strings.Contains(p.str(s), "(") {
		return p.name(s, "activity"), Value{}
	}

	activity, args, rest, ok := p.call(s)
	if !ok {
		return "", Value{}
	}

	if tail := p.trim(rest); tail.from < tail.to {
		p.fail(tail.from, "unexpected text after the undo call")
	}

	return activity, p.args(args)
}

// expr is the CEL source of s, checked for syntax; errors point into the
// text.
func (p *textParser) expr(s span) string {
	s = p.trim(s)
	src := p.str(s)

	if src == "" {
		p.fail(s.from, "empty expression")

		return ""
	}

	env, err := baseEnv()
	if err != nil {
		p.fail(s.from, err.Error())

		return src
	}

	if _, iss := env.Parse(src); iss.Err() != nil {
		for _, e := range iss.Errors() {
			at := s.from + offsetOf(src, e.Location.Line(), e.Location.Column())
			p.fail(at, "cel: "+e.Message)
		}
	}

	return src
}

// offsetOf is the byte offset of a CEL location (line from 1, column in
// characters from 0) in src.
func offsetOf(src string, line, column int) int {
	off := 0

	for l := 1; l < line; l++ {
		i := strings.IndexByte(src[off:], '\n')
		if i < 0 {
			return len(src)
		}

		off += i + 1
	}

	for n := 0; n < column && off < len(src); n++ {
		_, size := utf8.DecodeRuneInString(src[off:])
		off += size
	}

	return off
}

// split cuts s at sep outside brackets and strings.
func (p *textParser) split(s span, sep byte) []span {
	var out []span

	start, depth := s.from, 0

	for i := s.from; i < s.to; i++ {
		switch c := p.src[i]; c {
		case '"', '\'':
			i = skipString(p.src, i) - 1
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		default:
			if c == sep && depth == 0 {
				out = append(out, span{start, i})
				start = i + 1
			}
		}
	}

	return append(out, span{start, s.to})
}

// find is the offset of the first target outside brackets and strings in
// s, -1 when none.
func (p *textParser) find(s span, target string) int {
	depth := 0

	for i := s.from; i < s.to; i++ {
		switch p.src[i] {
		case '"', '\'':
			i = skipString(p.src, i) - 1
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		default:
			if depth == 0 && strings.HasPrefix(p.src[i:s.to], target) {
				return i
			}
		}
	}

	return -1
}

// matching is the offset of the bracket closing the one at open, -1 when
// it is not closed.
func matching(src string, open int) int {
	depth := 0

	for i := open; i < len(src); i++ {
		switch src[i] {
		case '"', '\'':
			i = skipString(src, i) - 1
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}

	return -1
}

// tripleQuote is the length of a triple-quote delimiter.
const tripleQuote = 3

// skipString is the offset past the CEL string literal whose quote is at
// i ('...', "...", triple-quoted; raw with an r prefix); past the end
// when it is not closed.
func skipString(src string, i int) int {
	quote := src[i]
	raw := i > 0 && (src[i-1] == 'r' || src[i-1] == 'R')
	delim := strings.Repeat(string(quote), tripleQuote)
	triple := strings.HasPrefix(src[i:], delim)

	j := i + 1
	if triple {
		j = i + tripleQuote
	}

	for j < len(src) {
		switch {
		case src[j] == '\\' && !raw:
			j += 2
		case triple && strings.HasPrefix(src[j:], delim):
			return j + tripleQuote
		case !triple && src[j] == quote:
			return j + 1
		case !triple && src[j] == '\n':
			return j
		default:
			j++
		}
	}

	return len(src) + 1
}

// blankComments replaces "//" comments outside strings with spaces:
// offsets stay those of the text.
func blankComments(text string) string {
	b := []byte(text)

	for i := 0; i < len(b); i++ {
		switch {
		case b[i] == '"' || b[i] == '\'':
			i = min(skipString(text, i), len(b)) - 1
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				b[i] = ' '
				i++
			}
		}
	}

	return string(b)
}

func unquote(s string) (string, error) {
	if strings.HasPrefix(s, "'") {
		inner := strings.ReplaceAll(s[1:len(s)-1], `\'`, `'`)
		inner = strings.ReplaceAll(inner, `"`, `\"`)
		s = `"` + inner + `"`
	}

	out, err := strconv.Unquote(s)
	if err != nil {
		return "", fmt.Errorf("unquote: %w", err)
	}

	return out, nil
}

// FormatBinding writes the canonical text of b: ParseBinding reads it back
// into b.
func FormatBinding(b Binding) string {
	var out strings.Builder

	out.WriteString(b.Hook + " :=\n")
	formatSteps(&out, b.Steps)

	if !b.Result.IsZero() {
		out.WriteString("  return " + formatResult(b.Result) + "\n")
	}

	return out.String()
}

// FormatRule writes the canonical text of r: ParseRule reads it back into
// r.
func FormatRule(r Rule) string {
	var out strings.Builder

	out.WriteString("on " + r.Event)

	if r.When != "" {
		out.WriteString(" when " + r.When)
	}

	out.WriteString(" :=\n")
	formatSteps(&out, r.Steps)

	return out.String()
}

func formatSteps(out *strings.Builder, steps []Step) {
	width := 0
	for i := range steps {
		width = max(width, utf8.RuneCountInString(steps[i].Name))
	}

	for i := range steps {
		s := &steps[i]
		pad := strings.Repeat(" ", width-utf8.RuneCountInString(s.Name))
		out.WriteString("  " + s.Name + pad + " = " + s.Activity + "(" + formatArgs(s.Input) + ")")

		if opts := formatOptions(*s); len(opts) > 0 {
			out.WriteString(" [" + strings.Join(opts, ", ") + "]")
		}

		out.WriteString("\n")
	}
}

func formatArgs(v Value) string {
	if v.Expr != "" {
		return v.Expr
	}

	return formatFields(v.Fields)
}

func formatResult(v Value) string {
	if v.Expr != "" {
		return v.Expr
	}

	return "{ " + formatFields(v.Fields) + " }"
}

func formatFields(fs []Field) string {
	parts := make([]string, 0, len(fs))

	for _, f := range fs {
		key := f.Name
		if !IsIdent(key) {
			key = strconv.Quote(key)
		}

		parts = append(parts, key+": "+f.Expr)
	}

	return strings.Join(parts, ", ")
}

func formatOptions(s Step) []string {
	var opts []string

	if s.When != "" {
		opts = append(opts, "when: "+s.When)
	}

	switch len(s.After) {
	case 0:
	case 1:
		opts = append(opts, "after: "+s.After[0])
	default:
		opts = append(opts, "after: ["+strings.Join(s.After, ", ")+"]")
	}

	if s.Undo != "" {
		undo := "undo: " + s.Undo
		if !s.UndoInput.IsZero() {
			undo += "(" + formatArgs(s.UndoInput) + ")"
		}

		opts = append(opts, undo)
	}

	if s.Retry.Attempts != 0 {
		opts = append(opts, "retry: "+strconv.Itoa(s.Retry.Attempts))
	}

	if s.Retry.InitialInterval != 0 {
		opts = append(opts, "retry_interval: "+s.Retry.InitialInterval.String())
	}

	if s.Retry.MaxInterval != 0 {
		opts = append(opts, "retry_max_interval: "+s.Retry.MaxInterval.String())
	}

	if s.Retry.Backoff != 0 {
		opts = append(opts, "retry_backoff: "+strconv.FormatFloat(s.Retry.Backoff, 'g', -1, 64))
	}

	if s.StartToClose != 0 {
		opts = append(opts, "timeout: "+s.StartToClose.String())
	}

	if s.Heartbeat != 0 {
		opts = append(opts, "heartbeat: "+s.Heartbeat.String())
	}

	return opts
}
