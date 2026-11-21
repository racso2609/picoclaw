package common

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
)

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
	if strings.Contains(content, "DSML") {
		return true
	}
	// Bare markers: "<" followed by two fullwidth pipes.
	return strings.Contains(content, "<"+dsmlFullwidthPipe+dsmlFullwidthPipe) ||
		strings.Contains(content, "</"+dsmlFullwidthPipe+dsmlFullwidthPipe)
}

// findDSMLBlocks locates every executable DSML block in content, skipping
// fenced code blocks and inline code spans.
func findDSMLBlocks(content string) []dsmlBlock {
	masked := maskCodeRegions(content)

	var blocks []dsmlBlock
	i := 0
	for {
		start, name, ok := findNextDSMLTag(masked, i, isDSMLBlockTag)
		if !ok {
			break
		}
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
	return blocks
}

func isDSMLBlockTag(name string) bool {
	switch name {
	case "tool_calls", "tool_call", "function_calls":
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
	nameStart := j
	for j < len(s) && isDSMLNameByte(s[j]) {
		j++
	}
	if j == nameStart {
		return "", 0, false
	}
	name = s[nameStart:j]
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
	if strings.HasPrefix(s[j:], "DSML") {
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

// findDSMLClose finds the closing tag for name starting at or after from.
func findDSMLClose(s string, from int, closeTag string) (int, bool) {
	want := strings.TrimPrefix(closeTag, "</")
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
		if strings.HasPrefix(s[j:], want) {
			k := j + len(want)
			for k < len(s) && s[k] != '>' {
				k++
			}
			if k < len(s) {
				return k + 1, true
			}
		}
	}
	return 0, false
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
		openEnd := start
		for openEnd < len(body) && body[openEnd] != '>' {
			openEnd++
		}
		if openEnd >= len(body) {
			break
		}
		openEnd++
		attrs := body[start:openEnd]
		toolName := dsmlAttr(attrs, "name")
		end, ok := findDSMLClose(body, openEnd, "</invoke")
		if !ok {
			break
		}
		inner := body[openEnd:end]
		if toolName != "" {
			if args := parseDSMLParameters(inner); args != nil {
				calls = append(calls, protocoltypes.ToolCall{
					ID:        newDSMLCallID(),
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
		openEnd := start
		for openEnd < len(inner) && inner[openEnd] != '>' {
			openEnd++
		}
		if openEnd >= len(inner) {
			break
		}
		openEnd++
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

// dsmlAttr reads an attribute value from a raw opening tag.
func dsmlAttr(tag, key string) string {
	for i := 0; i < len(tag); i++ {
		if tag[i] != key[0] {
			continue
		}
		if !strings.HasPrefix(tag[i:], key) {
			continue
		}
		j := i + len(key)
		for j < len(tag) && (tag[j] == ' ' || tag[j] == '=') {
			j++
		}
		if j >= len(tag) {
			return ""
		}
		if tag[j] != '"' && tag[j] != '\'' {
			return ""
		}
		quote := tag[j]
		j++
		start := j
		for j < len(tag) && tag[j] != quote {
			j++
		}
		return tag[start:j]
	}
	return ""
}

// maskCodeRegions replaces the contents of fenced code blocks and inline code
// spans with spaces, preserving offsets so indexes still map to the original.
func maskCodeRegions(s string) string {
	buf := []byte(s)
	i := 0
	for i < len(s) {
		if s[i] != '`' {
			i++
			continue
		}
		run := 0
		for i+run < len(s) && s[i+run] == '`' {
			run++
		}
		fence := strings.Repeat("`", run)
		closeIdx := strings.Index(s[i+run:], fence)
		if closeIdx < 0 {
			i += run
			continue
		}
		closeStart := i + run + closeIdx
		for k := i; k < closeStart+run; k++ {
			if buf[k] != '\n' {
				buf[k] = ' '
			}
		}
		i = closeStart + run
	}
	return string(buf)
}

func newDSMLCallID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "dsml_call"
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
