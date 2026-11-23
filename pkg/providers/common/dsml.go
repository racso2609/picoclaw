package common

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

// DSML (DeepSeek Markup Language) tool-call recovery.
//
// Some OpenAI-compatible upstreams (notably DeepSeek) occasionally emit their
// native tool-call markup as plain assistant *content* instead of returning
// structured `tool_calls`. The runtime then forwards the markup to the user as
// text and executes nothing.
//
// The canonical shape is:
//
//	<||DSML||tool_calls>
//	<||DSML||invoke name="exec">
//	<||DSML||parameter name="action" string="true">run</||DSML||parameter>
//	</||DSML||invoke>
//	</||DSML||tool_calls>
//
// where each pipe above stands for U+FF5C (FULLWIDTH VERTICAL LINE).
// Observed variants in the wild are the ASCII approximation with '|', a bare
// doubled fullwidth marker, a single pipe, and a space between the marker and
// the element name.
//
// The recovery is deliberately conservative:
//
//   - it only runs when the response carries no native tool calls;
//   - it never parses inside fenced code blocks or inline code spans, so a
//     model *talking about* DSML is not executed;
//   - it requires a well-formed invoke element with at least one parameter,
//     otherwise the content is left untouched.

const (
	dsmlFullwidthPipe = "\uFF5C"
	dsmlASCIIPipe     = "|"
	// dsmlWordSep is U+2581 LOWER ONE EIGHTH BLOCK, the word separator in
	// DeepSeek's tokenizer. A model that renders its own markup as text can
	// turn "tool_calls" into "tool\u2581calls".
	dsmlWordSep = "\u2581"
)

// dsmlNameAliases maps the spellings models actually emit to the canonical
// element name. DeepSeek's tokenizer splits "tool_calls" into "tool" + " calls",
// and models frequently drop the "tool" prefix altogether, so the wrapper
// arrives as "<..DSML.. calls>" instead of "<..DSML..tool_calls>".
var dsmlNameAliases = map[string]string{
	"calls":         "tool_calls",
	"call":          "tool_call",
	"toolcalls":     "tool_calls",
	"toolcall":      "tool_call",
	"functioncall":  "function_call",
	"functioncalls": "function_calls",
}

// normalizeDSMLName returns the canonical spelling of an element name.
func normalizeDSMLName(name string) string {
	if canon, ok := dsmlNameAliases[name]; ok {
		return canon
	}
	return name
}

// dsmlNameSepLen reports the length of a word separator inside an element name.
func dsmlNameSepLen(s string, i int) int {
	if i < len(s) && s[i] == ' ' {
		return 1
	}
	if strings.HasPrefix(s[i:], dsmlWordSep) {
		return len(dsmlWordSep)
	}
	return 0
}

// readDSMLName reads an element name at s[i]. A single word separator is
// re-joined when the two halves form a known compound, so "tool calls" and
// "tool\u2581calls" both read as "tool_calls". The returned name is canonical.
func readDSMLName(s string, i int) (string, int) {
	start := i
	for i < len(s) && isDSMLNameByte(s[i]) {
		i++
	}
	if i == start {
		return "", i
	}
	name := s[start:i]

	sep := dsmlNameSepLen(s, i)
	if sep == 0 {
		return normalizeDSMLName(name), i
	}
	k := i + sep
	restStart := k
	for k < len(s) && isDSMLNameByte(s[k]) {
		k++
	}
	if k == restStart {
		return normalizeDSMLName(name), i
	}
	joined := name + "_" + s[restStart:k]
	if canon := normalizeDSMLName(joined); canon != joined {
		return canon, k
	}
	switch joined {
	case "tool_calls", "tool_call", "function_calls", "function_call":
		return joined, k
	}
	return normalizeDSMLName(name), i
}

// containsFold reports whether s contains sub, ignoring ASCII case.
func containsFold(s, sub string) bool {
	n := len(sub)
	for i := 0; i+n <= len(s); i++ {
		if strings.EqualFold(s[i:i+n], sub) {
			return true
		}
	}
	return false
}

// dsmlBlock is a parsed tool-call block.
type dsmlBlock struct {
	start int
	end   int
	calls []protocoltypes.ToolCall
}

// RecoverDSMLToolCalls inspects an assistant response and, when it carries
// DSML markup in its content and no native tool calls, extracts the tool calls
// and strips the markup from the content.
//
// Only Content is inspected: markup emitted inside ReasoningContent is left
// alone on purpose, because that channel is never rendered to the user and
// executing tool calls recovered from a model's private reasoning would widen
// the attack surface for no user-visible benefit.
//
// It returns the (possibly modified) response and whether a recovery happened.
// The input response is not mutated.
func RecoverDSMLToolCalls(resp *protocoltypes.LLMResponse) (*protocoltypes.LLMResponse, bool) {
	if resp == nil || len(resp.ToolCalls) > 0 {
		return resp, false
	}
	content := resp.Content
	if content == "" || !looksLikeDSML(content) {
		return resp, false
	}

	blocks := findDSMLBlocks(content)
	if len(blocks) == 0 {
		return resp, false
	}

	var calls []protocoltypes.ToolCall
	var b strings.Builder
	last := 0
	for _, blk := range blocks {
		b.WriteString(content[last:blk.start])
		last = blk.end
		calls = append(calls, blk.calls...)
	}
	b.WriteString(content[last:])

	if len(calls) == 0 {
		return resp, false
	}

	out := *resp
	out.Content = strings.TrimSpace(b.String())
	out.ToolCalls = calls
	if out.FinishReason == "" || out.FinishReason == "stop" {
		out.FinishReason = "tool_calls"
	}
	return &out, true
}

// looksLikeDSML is a cheap pre-filter so that the (more expensive) masking and
// scanning only run for content that could plausibly carry the markup.
func looksLikeDSML(content string) bool {
	if containsFold(content, "DSML") {
		return true
	}
	// Bare markers: "<" followed by one or two fullwidth pipes.
	return strings.Contains(content, "<"+dsmlFullwidthPipe) ||
		strings.Contains(content, "</"+dsmlFullwidthPipe)
}

// findDSMLBlocks locates every executable DSML block in content, skipping
// fenced code blocks and inline code spans.
func findDSMLBlocks(content string) []dsmlBlock {
	masked := maskCodeRegions(content)

	var blocks []dsmlBlock
	sawWrapper := false
	i := 0
	for {
		start, name, ok := findNextDSMLTag(masked, i, isDSMLBlockTag)
		if !ok {
			break
		}
		sawWrapper = true
		closeTag := "</" + name
		end, ok := findDSMLClose(masked, start, closeTag)
		if !ok {
			// Unterminated block: stop here rather than swallow the rest.
			break
		}
		body := content[start:end]
		if calls := parseDSMLInvocations(body); len(calls) > 0 {
			blocks = append(blocks, dsmlBlock{start: start, end: end, calls: calls})
		}
		i = end
	}
	if len(blocks) == 0 && !sawWrapper {
		// Some models drop the wrapper element entirely and emit bare
		// <..DSML..invoke> tags. Recover those too: the invoke element is
		// self-describing, so the wrapper adds nothing but a failure mode.
		//
		// Only when no wrapper was seen at all: an unterminated wrapper must
		// stay untouched, and treating the invokes inside it as bare would
		// execute a block the model never closed.
		blocks = findBareDSMLInvocations(content, masked)
	}
	return blocks
}

// findBareDSMLInvocations recovers invoke blocks that appear without a
// tool_calls wrapper.
//
// Invoke elements are siblings, not nested, so each opener is resolved on its
// own: scan forward from the opener, skipping whole <parameter> elements (their
// values are text and may legitimately contain markup), and take the first
// </invoke>. If another <invoke> opener shows up before that close, the opener
// was never terminated — it must not swallow the close of the well-formed
// sibling that follows, so it is left visible in the content and the scan
// resumes at the sibling.
//
// A naive "first close after this opener" scan would delete the prose between
// an unterminated opener and the next valid invoke, and pairing openers and
// closers LIFO gets a mixed run backwards (two closed invokes around one
// unclosed opener end up executing the unclosed one with the arguments of the
// next). Resolving each opener independently keeps the scan linear and leaves
// orphaned openers visible.
func findBareDSMLInvocations(content, masked string) []dsmlBlock {
	var blocks []dsmlBlock
	i := 0
	for i < len(masked) {
		start, _, ok := findNextDSMLTag(masked, i, func(n string) bool { return n == "invoke" })
		if !ok {
			break
		}
		_, tagEnd, ok := parseDSMLOpenTag(masked, start)
		if !ok {
			i = start + 1
			continue
		}
		end, next, closed := findInvokeBodyEnd(masked, tagEnd)
		if !closed {
			if next > start {
				i = next
			} else {
				i = start + 1
			}
			continue
		}
		body := content[start:end]
		if calls := parseDSMLInvocations(body); len(calls) > 0 {
			blocks = append(blocks, dsmlBlock{start: start, end: end, calls: calls})
		}
		i = end
	}
	return blocks
}

// findInvokeBodyEnd scans forward from from (just past an invoke opener) and
// returns the offset just past the matching </invoke>. Parameter elements are
// skipped wholesale: their values are user-visible text and can contain markup
// that must not be mistaken for a structural tag.
//
// When another invoke opener is reached first, the element is unterminated:
// ok is false and next is that sibling's offset, so the caller can resume there
// without rescanning. When no close is found at all, ok is false and next is 0.
func findInvokeBodyEnd(s string, from int) (end int, next int, ok bool) {
	for i := from; i < len(s); i++ {
		if s[i] != '<' {
			continue
		}
		name, isClose, tagEnd, ok := parseDSMLTagAt(s, i)
		if !ok {
			continue
		}
		switch {
		case !isClose && name == "parameter":
			closeEnd, found := findNextDSMLClose(s, tagEnd, "parameter")
			if !found {
				return 0, 0, false
			}
			i = closeEnd - 1
		case isClose && name == "invoke":
			return tagEnd, 0, true
		case !isClose && name == "invoke":
			return 0, i, false
		}
	}
	return 0, 0, false
}

// parseDSMLTagAt parses a DSML opening or closing tag starting at s[start] ==
// '<'. It reports the canonical element name, whether the tag is a closer, and
// the offset just past the closing '>'.
func parseDSMLTagAt(s string, start int) (name string, isClose bool, end int, ok bool) {
	j := start + 1
	if j < len(s) && s[j] == '/' {
		isClose = true
		j++
	}
	markerLen := dsmlMarkerLen(s, j)
	if markerLen == 0 {
		return "", false, 0, false
	}
	j += markerLen
	for j < len(s) && s[j] == ' ' {
		j++
	}
	name, j = readDSMLName(s, j)
	if name == "" {
		return "", false, 0, false
	}
	for j < len(s) && s[j] != '>' {
		if s[j] == '\n' {
			return "", false, 0, false
		}
		j++
	}
	if j >= len(s) {
		return "", false, 0, false
	}
	return name, isClose, j + 1, true
}

// findNextDSMLClose returns the offset just past the next closing tag for name
// at or after from.
func findNextDSMLClose(s string, from int, name string) (int, bool) {
	want := normalizeDSMLName(name)
	for i := from; i < len(s); i++ {
		if s[i] != '<' {
			continue
		}
		j := i + 1
		if j < len(s) && s[j] == '/' {
			j++
		} else {
			continue
		}
		markerLen := dsmlMarkerLen(s, j)
		if markerLen == 0 {
			continue
		}
		j += markerLen
		for j < len(s) && s[j] == ' ' {
			j++
		}
		got, j := readDSMLName(s, j)
		if got == "" || got != want {
			continue
		}
		for j < len(s) && s[j] != '>' {
			if s[j] == '\n' {
				break
			}
			j++
		}
		if j < len(s) && s[j] == '>' {
			return j + 1, true
		}
	}
	return 0, false
}

func isDSMLBlockTag(name string) bool {
	switch name {
	case "tool_calls", "tool_call", "function_calls", "function_call":
		return true
	}
	return false
}

// findNextDSMLTag returns the offset of the next opening DSML tag whose element
// name satisfies accept.
func findNextDSMLTag(s string, from int, accept func(string) bool) (int, string, bool) {
	for i := from; i < len(s); i++ {
		if s[i] != '<' {
			continue
		}
		name, _, ok := parseDSMLOpenTag(s, i)
		if !ok {
			continue
		}
		if accept(name) {
			return i, name, true
		}
	}
	return 0, "", false
}

// parseDSMLOpenTag parses an opening tag starting at s[start] == '<'.
func parseDSMLOpenTag(s string, start int) (name string, end int, ok bool) {
	j := start + 1
	markerLen := dsmlMarkerLen(s, j)
	if markerLen == 0 {
		return "", 0, false
	}
	j += markerLen
	for j < len(s) && s[j] == ' ' {
		j++
	}
	name, j = readDSMLName(s, j)
	if name == "" {
		return "", 0, false
	}
	// Skip attributes up to '>'
	for j < len(s) && s[j] != '>' {
		if s[j] == '\n' {
			return "", 0, false
		}
		j++
	}
	if j >= len(s) {
		return "", 0, false
	}
	return name, j + 1, true
}

// dsmlMarkerLen reports the length of a DSML marker at s[i], or 0 if there is
// none. Accepted: U+FF5C, "||", a single "|", and combinations thereof, with an
// optional literal "DSML" spelling.
func dsmlMarkerLen(s string, i int) int {
	j := i
	consumed := false
	for j < len(s) {
		switch {
		case strings.HasPrefix(s[j:], dsmlFullwidthPipe):
			j += len(dsmlFullwidthPipe)
			consumed = true
		case strings.HasPrefix(s[j:], dsmlASCIIPipe+dsmlASCIIPipe):
			j += 2
			consumed = true
		case strings.HasPrefix(s[j:], dsmlASCIIPipe):
			j++
			consumed = true
		default:
			return markerLenOrZero(j, i, consumed, s)
		}
	}
	return markerLenOrZero(j, i, consumed, s)
}

func markerLenOrZero(j, i int, consumed bool, s string) int {
	if !consumed {
		return 0
	}
	// Optional "DSML" spelling right after the marker(s).
	if len(s) >= j+len("DSML") && strings.EqualFold(s[j:j+len("DSML")], "DSML") {
		j += len("DSML")
		// Trailing marker(s) after DSML, e.g. doubled fullwidth pipe.
		for j < len(s) {
			if strings.HasPrefix(s[j:], dsmlFullwidthPipe) {
				j += len(dsmlFullwidthPipe)
				continue
			}
			if strings.HasPrefix(s[j:], dsmlASCIIPipe) {
				j++
				continue
			}
			break
		}
	}
	return j - i
}

func isDSMLNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

// findDSMLClose finds the closing tag for closeTag (e.g. "</invoke") starting at
// or after from. It is the closeTag-flavoured spelling of findNextDSMLClose.
func findDSMLClose(s string, from int, closeTag string) (int, bool) {
	return findNextDSMLClose(s, from, strings.TrimPrefix(closeTag, "</"))
}

// parseDSMLInvocations extracts every invoke block.
func parseDSMLInvocations(body string) []protocoltypes.ToolCall {
	var calls []protocoltypes.ToolCall
	i := 0
	for {
		start, _, ok := findNextDSMLTag(body, i, func(n string) bool { return n == "invoke" })
		if !ok {
			break
		}
		openEnd := findTagEnd(body, start)
		if openEnd < 0 {
			break
		}
		attrs := body[start:openEnd]
		toolName := dsmlAttr(attrs, "name")
		// Resolve this invoke on its own: an unterminated opener must not take
		// the close of the well-formed sibling that follows it.
		end, next, closed := findInvokeBodyEnd(body, openEnd)
		if !closed {
			if next > start {
				i = next
				continue
			}
			break
		}
		inner := body[openEnd:end]
		if toolName != "" {
			if args := parseDSMLParameters(inner); args != nil {
				calls = append(calls, protocoltypes.ToolCall{
					ID:        newDSMLCallID(len(calls)),
					Type:      "function",
					Name:      toolName,
					Arguments: args,
					Function: &protocoltypes.FunctionCall{
						Name:      toolName,
						Arguments: mustJSON(args),
					},
				})
			}
		}
		i = end
	}
	return calls
}

// parseDSMLParameters extracts parameter elements.
func parseDSMLParameters(inner string) map[string]any {
	args := map[string]any{}
	i := 0
	for {
		start, _, ok := findNextDSMLTag(inner, i, func(n string) bool { return n == "parameter" })
		if !ok {
			break
		}
		openEnd := findTagEnd(inner, start)
		if openEnd < 0 {
			break
		}
		attrs := inner[start:openEnd]
		paramName := dsmlAttr(attrs, "name")
		isString := strings.EqualFold(dsmlAttr(attrs, "string"), "true")
		end, ok := findDSMLClose(inner, openEnd, "</parameter")
		if !ok {
			break
		}
		raw := inner[openEnd:end]
		// Trim the closing tag from raw.
		if idx := strings.LastIndex(raw, "</"); idx >= 0 {
			raw = raw[:idx]
		}
		if paramName != "" {
			args[paramName] = dsmlParamValue(raw, isString)
		}
		i = end
	}
	if len(args) == 0 {
		return nil
	}
	return args
}

// dsmlParamValue decodes a parameter value. Values flagged string="true" are
// taken verbatim; anything else is decoded as JSON when possible.
func dsmlParamValue(raw string, isString bool) any {
	if isString {
		return raw
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return raw
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
		return decoded
	}
	return raw
}

// findTagEnd returns the offset just past the closing '>' of the tag that starts
// at s[start], skipping over quoted attribute values so that a '>' inside a
// value does not truncate the tag. It returns -1 when the tag is unterminated
// or spans a newline.
func findTagEnd(s string, start int) int {
	var quote byte
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			// Honour backslash escapes so `label="a\"b"` does not end the
			// value early and swallow the rest of the tag.
			if c == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return i + 1
		case c == '\n':
			return -1
		}
	}
	return -1
}

// dsmlAttr reads an attribute value from a raw opening tag. The tag is
// tokenised (key=value pairs) rather than substring-searched, so a key that
// appears inside another attribute's value is not mistaken for the real one.
func dsmlAttr(tag, key string) string {
	i := 0
	for i < len(tag) {
		// Skip anything that cannot start a key.
		for i < len(tag) && !isDSMLAttrKeyByte(tag[i]) {
			i++
		}
		keyStart := i
		for i < len(tag) && isDSMLAttrKeyByte(tag[i]) {
			i++
		}
		if keyStart == i {
			break
		}
		name := tag[keyStart:i]
		// Optional whitespace before '='.
		j := i
		for j < len(tag) && (tag[j] == ' ' || tag[j] == '\t') {
			j++
		}
		if j >= len(tag) || tag[j] != '=' {
			// Not an attribute (bare word such as the element name); skip it.
			continue
		}
		j++
		for j < len(tag) && (tag[j] == ' ' || tag[j] == '\t') {
			j++
		}
		if j >= len(tag) {
			return ""
		}
		var value string
		if tag[j] == '"' || tag[j] == '\'' {
			quote := tag[j]
			j++
			valStart := j
			for j < len(tag) && tag[j] != quote {
				j++
			}
			value = tag[valStart:j]
			if j < len(tag) {
				j++
			}
		} else {
			valStart := j
			for j < len(tag) && tag[j] != ' ' && tag[j] != '\t' && tag[j] != '>' {
				j++
			}
			value = tag[valStart:j]
		}
		if name == key {
			return value
		}
		i = j
	}
	return ""
}

func isDSMLAttrKeyByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-' || b == ':'
}

// maskCodeRegions replaces the contents of fenced code blocks and inline code
// spans with spaces, preserving offsets so indexes still map to the original.
//
// Fences follow CommonMark closely enough for the guard to be sound: both
// backtick and tilde fences are recognised, the closing fence must use the same
// character and be at least as long as the opener, and a fence that is never
// closed masks everything to the end of the string (a model that opens a fence
// and forgets to close it must not get its markup executed).
func maskCodeRegions(s string) string {
	buf := []byte(s)
	blank := func(from, to int) {
		for k := from; k < to && k < len(buf); k++ {
			if buf[k] != '\n' {
				buf[k] = ' '
			}
		}
	}
	i := 0
	for i < len(s) {
		if s[i] != '`' && s[i] != '~' {
			i++
			continue
		}
		ch := s[i]
		run := 0
		for i+run < len(s) && s[i+run] == ch {
			run++
		}
		// A fence opener sits at most 3 spaces into its line and uses a run of
		// three or more characters. Anything else is an inline code span.
		lineStart := strings.LastIndexByte(s[:i], '\n') + 1
		atLineStart := i-lineStart <= 3 && strings.TrimLeft(s[lineStart:i], " ") == ""
		if run >= 3 && atLineStart {
			closeEnd, ok := findFenceClose(s, i+run, ch, run)
			if !ok {
				// Unterminated fence: mask the remainder.
				blank(i, len(buf))
				break
			}
			blank(i, closeEnd)
			i = closeEnd
			continue
		}
		if ch != '`' {
			i += run
			continue
		}
		// Inline code span: the closing run must have the same length.
		closeStart, ok := findInlineCodeClose(s, i+run, run)
		if !ok {
			i += run
			continue
		}
		blank(i, closeStart+run)
		i = closeStart + run
	}
	return string(buf)
}

// findFenceClose locates a closing fence made of at least n copies of ch and
// returns the offset just past it.
func findFenceClose(s string, from int, ch byte, n int) (int, bool) {
	i := from
	for i < len(s) {
		nl := strings.IndexByte(s[i:], '\n')
		if nl < 0 {
			return 0, false
		}
		lineStart := i + nl + 1
		if lineStart >= len(s) {
			return 0, false
		}
		j := lineStart
		for j < len(s) && j-lineStart < 3 && s[j] == ' ' {
			j++
		}
		run := 0
		for j+run < len(s) && s[j+run] == ch {
			run++
		}
		if run >= n {
			// The rest of the line must be blank for it to be a closing fence.
			k := j + run
			for k < len(s) && s[k] == ' ' {
				k++
			}
			if k >= len(s) || s[k] == '\n' {
				return k, true
			}
		}
		i = lineStart
	}
	return 0, false
}

// findInlineCodeClose locates a backtick run of exactly n characters, which is
// what CommonMark requires to close an inline code span.
func findInlineCodeClose(s string, from, n int) (int, bool) {
	i := from
	for i < len(s) {
		if s[i] != '`' {
			i++
			continue
		}
		run := 0
		for i+run < len(s) && s[i+run] == '`' {
			run++
		}
		if run == n {
			return i, true
		}
		i += run
	}
	return 0, false
}

func newDSMLCallID(seq int) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to the position within the block so IDs stay unique in the
		// history even when the RNG is unavailable.
		return "dsml_call_" + strconv.Itoa(seq)
	}
	return "dsml_" + hex.EncodeToString(b[:])
}

func mustJSON(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
