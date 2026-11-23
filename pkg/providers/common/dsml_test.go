package common

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

const (
	fw = "\uFF5C"
)

func dsmlCanonical(body string) string {
	return "<" + fw + fw + "DSML" + fw + fw + "tool_calls>\n" + body + "\n</" + fw + fw + "DSML" + fw + fw + "tool_calls>"
}

func dsmlInvoke(name, params string) string {
	return "<" + fw + fw + "DSML" + fw + fw + "invoke name=\"" + name + "\">\n" + params + "\n</" + fw + fw + "DSML" + fw + fw + "invoke>"
}

func dsmlParam(name, value string, isString bool) string {
	attr := ""
	if isString {
		attr = ` string="true"`
	}
	return "<" + fw + fw + "DSML" + fw + fw + "parameter name=\"" + name + "\"" + attr + ">" + value + "</" + fw + fw + "DSML" + fw + fw + "parameter>"
}

func dsmlInvokeLiteral(literal, name, params string) string {
	return "<" + fw + fw + literal + fw + fw + "invoke name=\"" + name + "\">\n" + params + "\n</" + fw + fw + literal + fw + fw + "invoke>"
}

func dsmlParamLiteral(literal, name, value string, isString bool) string {
	attr := ""
	if isString {
		attr = ` string="true"`
	}
	return "<" + fw + fw + literal + fw + fw + "parameter name=\"" + name + "\"" + attr + ">" + value + "</" + fw + fw + literal + fw + fw + "parameter>"
}

func TestRecoverDSMLToolCalls_CanonicalFullwidth(t *testing.T) {
	content := "Voy a mirar.\n\n" + dsmlCanonical(
		dsmlInvoke("exec", dsmlParam("action", "run", true)+"\n"+dsmlParam("command", "ls -la", true)),
	)

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content, FinishReason: "stop"})
	if !ok {
		t.Fatalf("expected recovery, got none")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.Name != "exec" {
		t.Fatalf("Name = %q, want exec", tc.Name)
	}
	if tc.Arguments["action"] != "run" {
		t.Fatalf("action = %v, want run", tc.Arguments["action"])
	}
	if tc.Arguments["command"] != "ls -la" {
		t.Fatalf("command = %v, want ls -la", tc.Arguments["command"])
	}
	if tc.Function == nil || tc.Function.Name != "exec" {
		t.Fatalf("Function not populated: %+v", tc.Function)
	}
	if tc.ID == "" {
		t.Fatalf("ID must not be empty")
	}
	if strings.Contains(resp.Content, "DSML") {
		t.Fatalf("markup not stripped: %q", resp.Content)
	}
	if resp.Content != "Voy a mirar." {
		t.Fatalf("content = %q, want %q", resp.Content, "Voy a mirar.")
	}
	if resp.FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q, want tool_calls", resp.FinishReason)
	}
}

func TestRecoverDSMLToolCalls_ASCIIVariant(t *testing.T) {
	content := "<||DSML||tool_calls>\n<||DSML||invoke name=\"exec\">\n" +
		"<||DSML||parameter name=\"action\" string=\"true\">run</||DSML||parameter>\n" +
		"</||DSML||invoke>\n</||DSML||tool_calls>"

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok {
		t.Fatalf("expected recovery for ASCII variant")
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Arguments["action"] != "run" {
		t.Fatalf("unexpected tool calls: %+v", resp.ToolCalls)
	}
}

func TestRecoverDSMLToolCalls_BareDoubledMarker(t *testing.T) {
	p := fw + fw
	content := "<" + p + "tool_calls>\n<" + p + "invoke name=\"read_file\">\n" +
		"<" + p + "parameter name=\"path\" string=\"true\">/etc/hosts</" + p + "parameter>\n" +
		"</" + p + "invoke>\n</" + p + "tool_calls>"

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok {
		t.Fatalf("expected recovery for bare doubled marker")
	}
	if resp.ToolCalls[0].Name != "read_file" || resp.ToolCalls[0].Arguments["path"] != "/etc/hosts" {
		t.Fatalf("unexpected: %+v", resp.ToolCalls[0])
	}
}

func TestRecoverDSMLToolCalls_MultipleInvocations(t *testing.T) {
	content := dsmlCanonical(
		dsmlInvoke("exec", dsmlParam("action", "run", true)+"\n"+dsmlParam("command", "pwd", true)) + "\n" +
			dsmlInvoke("read_file", dsmlParam("path", "/tmp/a.txt", true)),
	)

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok {
		t.Fatalf("expected recovery")
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("len = %d, want 2", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "exec" || resp.ToolCalls[1].Name != "read_file" {
		t.Fatalf("names = %q, %q", resp.ToolCalls[0].Name, resp.ToolCalls[1].Name)
	}
	if resp.ToolCalls[0].ID == resp.ToolCalls[1].ID {
		t.Fatalf("tool call IDs must be unique")
	}
}

func TestRecoverDSMLToolCalls_NonStringParamDecodedAsJSON(t *testing.T) {
	content := dsmlCanonical(
		dsmlInvoke("exec", dsmlParam("action", "run", true)+"\n"+dsmlParam("timeout", "30", false)),
	)

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok {
		t.Fatalf("expected recovery")
	}
	if got, isFloat := resp.ToolCalls[0].Arguments["timeout"].(float64); !isFloat || got != 30 {
		t.Fatalf("timeout = %#v, want float64(30)", resp.ToolCalls[0].Arguments["timeout"])
	}
}

func TestRecoverDSMLToolCalls_SkipsWhenNativeToolCallsPresent(t *testing.T) {
	native := []protocoltypes.ToolCall{{ID: "call_1", Name: "exec"}}
	content := dsmlCanonical(dsmlInvoke("exec", dsmlParam("action", "run", true)))

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{
		Content:   content,
		ToolCalls: native,
	})
	if ok {
		t.Fatalf("must not recover when native tool calls are present")
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" {
		t.Fatalf("native tool calls must be untouched: %+v", resp.ToolCalls)
	}
	if resp.Content != content {
		t.Fatalf("content must be untouched when no recovery happens")
	}
}

func TestRecoverDSMLToolCalls_IgnoresMarkupInsideFencedCodeBlock(t *testing.T) {
	block := dsmlCanonical(dsmlInvoke("exec", dsmlParam("action", "run", true)))
	content := "El formato es:\n\n```\n" + block + "\n```\n\nY eso es todo."

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if ok {
		t.Fatalf("must not execute markup inside a fenced code block")
	}
	if resp.Content != content {
		t.Fatalf("content must be untouched")
	}
}

func TestRecoverDSMLToolCalls_IgnoresMarkupInsideInlineCode(t *testing.T) {
	block := dsmlCanonical(dsmlInvoke("exec", dsmlParam("action", "run", true)))
	content := "El marcador `" + block + "` no debe ejecutarse."

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if ok {
		t.Fatalf("must not execute markup inside an inline code span")
	}
	if resp.Content != content {
		t.Fatalf("content must be untouched")
	}
}

func TestRecoverDSMLToolCalls_ExecutesOutsideButNotInsideCode(t *testing.T) {
	real := dsmlCanonical(dsmlInvoke("exec", dsmlParam("action", "run", true)))
	quoted := dsmlCanonical(dsmlInvoke("exec", dsmlParam("action", "list", true)))
	content := "Primero:\n\n```\n" + quoted + "\n```\n\nAhora de verdad:\n\n" + real

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok {
		t.Fatalf("expected recovery of the block outside the fence")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Arguments["action"] != "run" {
		t.Fatalf("action = %v, want run (the one outside the fence)", resp.ToolCalls[0].Arguments["action"])
	}
	if !strings.Contains(resp.Content, "```") {
		t.Fatalf("the fenced example must survive in the content")
	}
	if !strings.Contains(resp.Content, quoted) {
		t.Fatalf("fenced example must be preserved verbatim, got %q", resp.Content)
	}
}

func TestRecoverDSMLToolCalls_NoMarkup(t *testing.T) {
	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: "hola"})
	if ok || resp.Content != "hola" {
		t.Fatalf("plain content must be untouched")
	}
}

func TestRecoverDSMLToolCalls_NilResponse(t *testing.T) {
	resp, ok := RecoverDSMLToolCalls(nil)
	if ok || resp != nil {
		t.Fatalf("nil response must be a no-op")
	}
}

func TestRecoverDSMLToolCalls_UnterminatedBlockIsLeftAlone(t *testing.T) {
	content := "<" + fw + fw + "DSML" + fw + fw + "tool_calls>\n<" + fw + fw + "DSML" + fw + fw + "invoke name=\"exec\">\n" +
		dsmlParam("action", "run", true)

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if ok {
		t.Fatalf("unterminated block must not be executed")
	}
	if resp.Content != content {
		t.Fatalf("content must be untouched")
	}
}

// TestRecoverDSMLToolCalls_UnterminatedBlockDoesNotSwallowFollowingBlock is the
// falsifiable counterpart of the test above. A valid block followed by an
// unterminated one must recover exactly one call and leave the trailing markup
// visible. Reverting the "stop here rather than swallow the rest" guard in
// findDSMLBlocks (e.g. returning early or dropping the block) breaks this.
func TestRecoverDSMLToolCalls_UnterminatedBlockDoesNotSwallowFollowingBlock(t *testing.T) {
	valid := dsmlCanonical(dsmlInvoke("exec", dsmlParam("command", "echo ok", true)))
	unterminated := "<" + fw + fw + "DSML" + fw + fw + "tool_calls>\n" +
		dsmlInvoke("exec", dsmlParam("command", "echo broken", true))
	content := valid + "\n\n" + unterminated

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok {
		t.Fatalf("the valid block must be recovered")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(resp.ToolCalls))
	}
	if got := resp.ToolCalls[0].Arguments["command"]; got != "echo ok" {
		t.Fatalf("recovered command = %v, want 'echo ok'", got)
	}
	if !strings.Contains(resp.Content, "echo broken") {
		t.Fatalf("unterminated markup must stay visible, got %q", resp.Content)
	}
	if strings.Contains(resp.Content, "echo ok") {
		t.Fatalf("the recovered block must be stripped from content, got %q", resp.Content)
	}
}

func TestRecoverDSMLToolCalls_InvokeWithoutParametersIsSkipped(t *testing.T) {
	content := dsmlCanonical(dsmlInvoke("exec", ""))

	if _, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content}); ok {
		t.Fatalf("invoke without parameters must be skipped")
	}
}

func TestRecoverDSMLToolCalls_DoesNotMutateInput(t *testing.T) {
	content := dsmlCanonical(dsmlInvoke("exec", dsmlParam("action", "run", true)))
	in := &protocoltypes.LLMResponse{Content: content, FinishReason: "stop"}

	out, ok := RecoverDSMLToolCalls(in)
	if !ok {
		t.Fatalf("expected recovery")
	}
	if in.Content != content || len(in.ToolCalls) != 0 || in.FinishReason != "stop" {
		t.Fatalf("input response was mutated: %+v", in)
	}
	if out == in {
		t.Fatalf("recovered response must be a copy")
	}
}

func TestRecoverDSMLToolCalls_KeepsExistingFinishReason(t *testing.T) {
	content := dsmlCanonical(dsmlInvoke("exec", dsmlParam("action", "run", true)))
	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content, FinishReason: "length"})
	if !ok {
		t.Fatalf("expected recovery")
	}
	if resp.FinishReason != "length" {
		t.Fatalf("FinishReason = %q, want length", resp.FinishReason)
	}
}

// string="true" must be honoured: a JSON-looking value stays a raw string.
func TestRecoverDSMLToolCalls_StringParamKeptVerbatim(t *testing.T) {
	content := dsmlCanonical(
		dsmlInvoke("exec", dsmlParam("command", `{"a":1}`, true)),
	)

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok {
		t.Fatalf("expected recovery")
	}
	got, isString := resp.ToolCalls[0].Arguments["command"].(string)
	if !isString {
		t.Fatalf("command = %#v, want raw string", resp.ToolCalls[0].Arguments["command"])
	}
	if got != `{"a":1}` {
		t.Fatalf("command = %q, want the verbatim JSON text", got)
	}
}

// TestRecoverDSMLToolCalls_IgnoresMarkupInsideTildeFence covers the tilde fence
// form, which the first version of maskCodeRegions did not recognise at all.
func TestRecoverDSMLToolCalls_IgnoresMarkupInsideTildeFence(t *testing.T) {
	content := "~~~\n" + dsmlCanonical(dsmlInvoke("exec", dsmlParam("command", "rm -rf /", true))) + "\n~~~"
	if _, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content}); ok {
		t.Fatalf("must not execute markup inside a tilde fence")
	}
}

// TestRecoverDSMLToolCalls_ClosingFenceMustBeAtLeastAsLong covers fences whose
// closing run is shorter than the opener: CommonMark does not close the block
// there, so the content stays inside the fence and must not execute.
func TestRecoverDSMLToolCalls_ClosingFenceMustBeAtLeastAsLong(t *testing.T) {
	blk := dsmlCanonical(dsmlInvoke("exec", dsmlParam("command", "rm -rf /", true)))
	for _, content := range []string{
		"````\n" + blk + "\n```",
		"`````\n" + blk + "\n```",
		"~~~~\n" + blk + "\n~~~",
	} {
		if _, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content}); ok {
			t.Fatalf("must not execute markup inside an unclosed fence: %q", content)
		}
	}
}

// TestRecoverDSMLToolCalls_UnterminatedFenceMasksRest covers the "model opens a
// fence and never closes it" case: everything after the opener is fenced.
func TestRecoverDSMLToolCalls_UnterminatedFenceMasksRest(t *testing.T) {
	content := "```\n" + dsmlCanonical(dsmlInvoke("exec", dsmlParam("command", "rm -rf /", true)))
	if _, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content}); ok {
		t.Fatalf("must not execute markup inside an unterminated fence")
	}
}

// TestRecoverDSMLToolCalls_AttributeValueWithGT covers a '>' inside a quoted
// attribute value, which used to truncate the tag and lose the whole call.
func TestRecoverDSMLToolCalls_AttributeValueWithGT(t *testing.T) {
	param := "<" + fw + fw + "DSML" + fw + fw + "parameter" + ` label="a>b" name="command" string="true">ls</` +
		fw + fw + "DSML" + fw + fw + "parameter>"
	content := dsmlCanonical(dsmlInvoke("exec", param))

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok {
		t.Fatalf("a '>' inside an attribute value must not lose the call")
	}
	if got := resp.ToolCalls[0].Arguments["command"]; got != "ls" {
		t.Fatalf("command = %v, want ls", got)
	}
}

// TestRecoverDSMLToolCalls_AttributeNameInsideValue covers a parameter named
// "name" appearing inside another attribute's value; the real name attribute
// must win.
func TestRecoverDSMLToolCalls_AttributeNameInsideValue(t *testing.T) {
	param := "<" + fw + fw + "DSML" + fw + fw + "parameter" + ` label="name=foo" name="command" string="true">ls</` +
		fw + fw + "DSML" + fw + fw + "parameter>"
	content := dsmlCanonical(dsmlInvoke("exec", param))

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok {
		t.Fatalf("an attribute name inside a value must not lose the call")
	}
	if got := resp.ToolCalls[0].Arguments["command"]; got != "ls" {
		t.Fatalf("command = %v, want ls (got args %v)", got, resp.ToolCalls[0].Arguments)
	}
}

// TestRecoverDSMLToolCalls_UniqueIDsWithinBlock guards the fallback path used
// when the RNG fails: IDs must stay unique so the history is not corrupted.
func TestRecoverDSMLToolCalls_UniqueIDsWithinBlock(t *testing.T) {
	content := dsmlCanonical(
		dsmlInvoke("exec", dsmlParam("command", "ls", true)) + "\n" +
			dsmlInvoke("exec", dsmlParam("command", "pwd", true)))

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok || len(resp.ToolCalls) != 2 {
		t.Fatalf("expected 2 recovered calls, got %d (ok=%v)", len(resp.ToolCalls), ok)
	}
	if resp.ToolCalls[0].ID == resp.ToolCalls[1].ID {
		t.Fatalf("tool call IDs must be unique, both are %q", resp.ToolCalls[0].ID)
	}
}

// TestRecoverDSMLToolCalls_EscapedQuoteInAttributeValue covers a backslash
// escaped quote inside an attribute value. Without the escape handling in
// findTagEnd the quote closes the value early, the tag never terminates and the
// whole call is lost.
func TestRecoverDSMLToolCalls_EscapedQuoteInAttributeValue(t *testing.T) {
	param := "<" + fw + fw + "DSML" + fw + fw + "parameter" + ` label="a\"b" name="command" string="true">ls</` +
		fw + fw + "DSML" + fw + fw + "parameter>"
	content := dsmlCanonical(dsmlInvoke("exec", param))

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok {
		t.Fatalf("an escaped quote inside an attribute value must not lose the call")
	}
	if got := resp.ToolCalls[0].Arguments["command"]; got != "ls" {
		t.Fatalf("command = %v, want ls (args %v)", got, resp.ToolCalls[0].Arguments)
	}
}

// ---------------------------------------------------------------------------
// Block-name variants.
//
// DeepSeek's tokenizer splits "tool_calls" into "tool" + " calls" (U+2581 is its
// word separator), and models routinely drop the "tool" prefix or lowercase the
// literal marker. The wrapper then arrives as "<..DSML.. calls>" instead of
// "<..DSML..tool_calls>", which the original matcher rejected: the markup was
// forwarded to the user as text and nothing executed.
// ---------------------------------------------------------------------------

// dsmlWrapper builds a wrapper element with an arbitrary (non-canonical) name,
// using the literal "DSML" spelling between single fullwidth pipes, which is the
// shape observed in production.
func dsmlWrapper(name, body string) string {
	return dsmlWrapperLiteral("DSML", name, body)
}

// dsmlWrapperLiteral is dsmlWrapper with an explicit spelling of the marker.
func dsmlWrapperLiteral(literal, name, body string) string {
	return "<" + fw + literal + fw + " " + name + ">\n" + body + "\n</" + fw + literal + fw + " " + name + ">"
}

func TestRecoverDSMLToolCalls_BlockNameVariants(t *testing.T) {
	body := dsmlInvoke("exec", dsmlParam("command", "ls -la", true))
	// The lowercase case needs the whole payload lowercase: with a "DSML"
	// invoke inside, the bare-invoke fallback recovers the call anyway and the
	// case-insensitivity of the marker could be removed without a failure.
	lowerBody := dsmlInvokeLiteral("dsml", "exec", dsmlParamLiteral("dsml", "command", "ls -la", true))

	cases := []struct{ name, wrapper, literal, body string }{
		{"dropped tool prefix", "calls", "DSML", body},
		{"space separator", "tool calls", "DSML", body},
		{"tokenizer word separator", "tool\u2581calls", "DSML", body},
		{"lowercase marker", "tool_calls", "dsml", lowerBody},
		{"function_call singular", "function_call", "DSML", body},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			content := dsmlWrapperLiteral(c.literal, c.wrapper, c.body)
			resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
			if !ok {
				t.Fatalf("wrapper %q must be recovered", c.wrapper)
			}
			if len(resp.ToolCalls) != 1 {
				t.Fatalf("len(ToolCalls) = %d, want 1", len(resp.ToolCalls))
			}
			if got := resp.ToolCalls[0].Arguments["command"]; got != "ls -la" {
				t.Fatalf("command = %v, want 'ls -la'", got)
			}
			if strings.Contains(resp.Content, "DSML") {
				t.Fatalf("markup must be stripped, got %q", resp.Content)
			}
		})
	}
}

// TestRecoverDSMLToolCalls_RealProductionPayload is the exact content that
// reached the user on 2026-11-23 (two agents, same shape): the wrapper is
// "<..DSML.. calls>" and the first invoke repeats the "action" parameter.
func TestRecoverDSMLToolCalls_RealProductionPayload(t *testing.T) {
	content := "\n\n" +
		"<" + fw + "DSML" + fw + " calls>\n" +
		"<" + fw + "DSML" + fw + " invoke name=\"exec\">\n" +
		"<" + fw + "DSML" + fw + " parameter name=\"action\" string=\"true\">run</" + fw + "DSML" + fw + " parameter>\n" +
		"<" + fw + "DSML" + fw + " parameter name=\"action\" string=\"true\">run</" + fw + "DSML" + fw + " parameter>\n" +
		"<" + fw + "DSML" + fw + " parameter name=\"command\" string=\"true\">echo hola</" + fw + "DSML" + fw + " parameter>\n" +
		"</" + fw + "DSML" + fw + " invoke>\n" +
		"<" + fw + "DSML" + fw + " invoke name=\"exec\">\n" +
		"<" + fw + "DSML" + fw + " parameter name=\"command\" string=\"true\">git diff main</" + fw + "DSML" + fw + " parameter>\n" +
		"</" + fw + "DSML" + fw + " invoke>\n" +
		"</" + fw + "DSML" + fw + " calls>"

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok {
		t.Fatal("the production payload must be recovered")
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("len(ToolCalls) = %d, want 2", len(resp.ToolCalls))
	}
	if got := resp.ToolCalls[0].Arguments["command"]; got != "echo hola" {
		t.Fatalf("first command = %v, want 'echo hola'", got)
	}
	// The duplicated "action" parameter must collapse to a single value rather
	// than producing a broken argument map.
	if got := resp.ToolCalls[0].Arguments["action"]; got != "run" {
		t.Fatalf("action = %v, want 'run'", got)
	}
	if got := resp.ToolCalls[1].Arguments["command"]; got != "git diff main" {
		t.Fatalf("second command = %v, want 'git diff main'", got)
	}
	if resp.FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q, want tool_calls", resp.FinishReason)
	}
}

// TestRecoverDSMLToolCalls_BareInvokeWithoutWrapper covers a model that emits
// the invoke element without the tool_calls wrapper at all.
func TestRecoverDSMLToolCalls_BareInvokeWithoutWrapper(t *testing.T) {
	content := "\n\n" + dsmlInvoke("exec", dsmlParam("command", "pwd", true))

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok {
		t.Fatal("a bare invoke must be recovered")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(resp.ToolCalls))
	}
	if resp.Content != "" {
		t.Fatalf("the invoke must be stripped from content, got %q", resp.Content)
	}
}

// TestRecoverDSMLToolCalls_BareInvokeInsideFenceIsIgnored is the falsifiable
// counterpart of the bare-invoke fallback: widening the matcher must not start
// executing markup that a model is merely quoting.
func TestRecoverDSMLToolCalls_BareInvokeInsideFenceIsIgnored(t *testing.T) {
	content := "Look at this:\n\n```\n" + dsmlInvoke("exec", dsmlParam("command", "rm -rf /", true)) + "\n```\n"

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if ok {
		t.Fatalf("markup inside a fence must never execute, got %d calls", len(resp.ToolCalls))
	}
	if resp.Content != content {
		t.Fatalf("content must be untouched")
	}
}

// TestRecoverDSMLToolCalls_BareInvokeWithoutParametersIsIgnored guards the
// fallback against executing a bare, parameterless invoke.
func TestRecoverDSMLToolCalls_BareInvokeWithoutParametersIsIgnored(t *testing.T) {
	content := "I would call " + dsmlInvoke("exec", "") + " here."

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if ok {
		t.Fatalf("a parameterless invoke must not execute, got %d calls", len(resp.ToolCalls))
	}
	if resp.Content != content {
		t.Fatalf("content must be untouched")
	}
}

// TestRecoverDSMLToolCalls_InvokeElementNameIsNotMerged is a regression guard
// for the name re-joining logic: "invoke name=..." must keep reading as the
// element "invoke" with a "name" attribute, never as "invoke_name".
func TestRecoverDSMLToolCalls_InvokeElementNameIsNotMerged(t *testing.T) {
	content := dsmlCanonical(dsmlInvoke("exec", dsmlParam("command", "ls", true)))

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok || len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 call, got %d (ok=%v)", len(resp.ToolCalls), ok)
	}
	if resp.ToolCalls[0].Name != "exec" {
		t.Fatalf("name = %q, want exec", resp.ToolCalls[0].Name)
	}
}

// TestRecoverDSMLToolCalls_LowercaseMarkerLiteral covers the literal marker
// spelled "dsml" instead of "DSML". Matching it case-sensitively drops the
// whole block.
func TestRecoverDSMLToolCalls_LowercaseMarkerLiteral(t *testing.T) {
	// The whole payload is lowercase, including the invoke element: otherwise
	// the bare-invoke fallback would recover the call and hide the regression.
	invoke := "<" + fw + "dsml" + fw + " invoke name=\"exec\">\n" +
		"<" + fw + "dsml" + fw + " parameter name=\"command\" string=\"true\">ls</" + fw + "dsml" + fw + " parameter>\n" +
		"</" + fw + "dsml" + fw + " invoke>"
	content := "<" + fw + "dsml" + fw + " tool_calls>\n" + invoke + "\n</" + fw + "dsml" + fw + " tool_calls>"

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok {
		t.Fatal("a lowercase 'dsml' marker must still be recovered")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(resp.ToolCalls))
	}
}

// TestRecoverDSMLToolCalls_SinglePipeMarkerWithoutLiteral covers the marker
// written as a single fullwidth pipe and no "DSML" spelling at all. The cheap
// pre-filter must not reject it before the real parser runs.
func TestRecoverDSMLToolCalls_SinglePipeMarkerWithoutLiteral(t *testing.T) {
	invoke := "<" + fw + "invoke name=\"exec\">\n" + "<" + fw + "parameter name=\"command\" string=\"true\">ls</" + fw + "parameter>\n</" + fw + "invoke>"
	content := "<" + fw + "tool_calls>\n" + invoke + "\n</" + fw + "tool_calls>"

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok {
		t.Fatal("a single-pipe marker must be recovered")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(resp.ToolCalls))
	}
	if got := resp.ToolCalls[0].Arguments["command"]; got != "ls" {
		t.Fatalf("command = %v, want ls", got)
	}
}

// ---------------------------------------------------------------------------
// Guards found by review: widening the matcher must not resurrect markup the
// model never closed.
// ---------------------------------------------------------------------------

// TestRecoverDSMLToolCalls_UnterminatedWrapperWithClosedInvokeIsLeftAlone is the
// falsifiable counterpart of TestRecoverDSMLToolCalls_UnterminatedBlockIsLeftAlone.
// The older test has no closed invoke inside, so it passed even when the
// bare-invoke fallback started executing the contents of an unclosed wrapper.
func TestRecoverDSMLToolCalls_UnterminatedWrapperWithClosedInvokeIsLeftAlone(t *testing.T) {
	content := "<" + fw + fw + "DSML" + fw + fw + "tool_calls>\n" +
		dsmlInvoke("exec", dsmlParam("command", "echo inside", true))

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if ok {
		t.Fatalf("an unterminated wrapper must never execute, got %d calls", len(resp.ToolCalls))
	}
	if resp.Content != content {
		t.Fatalf("content must be untouched, got %q", resp.Content)
	}
}

// TestRecoverDSMLToolCalls_UnclosedInvokeDoesNotSwallowLaterProse covers a
// close tag matching an opener that was never closed: taking it would delete the
// prose in between and execute the wrong command.
func TestRecoverDSMLToolCalls_UnclosedInvokeDoesNotSwallowLaterProse(t *testing.T) {
	content := "sintaxis: <" + fw + fw + "DSML" + fw + fw + "invoke name=\"exec\">\n" +
		"esto no lo cierro\n" +
		dsmlInvoke("exec", dsmlParam("command", "echo real", true))

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok {
		t.Fatal("the closed invoke must still be recovered")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(resp.ToolCalls))
	}
	if got := resp.ToolCalls[0].Arguments["command"]; got != "echo real" {
		t.Fatalf("command = %v, want 'echo real'", got)
	}
	if !strings.Contains(resp.Content, "esto no lo cierro") {
		t.Fatalf("prose between the unclosed opener and the close must survive, got %q", resp.Content)
	}
}

// TestRecoverDSMLToolCalls_BareParameterMentionsOpener is the bare variant of
// ParameterMentionsOpener: same rule, same outcome.
func TestRecoverDSMLToolCalls_BareParameterMentionsOpener(t *testing.T) {
	opener := "<" + fw + fw + "DSML" + fw + fw + "invoke name=\"exec\">"
	content := dsmlInvoke("exec", dsmlParam("command", "echo '"+opener+" es la sintaxis'", true))

	resp, _ := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("len(ToolCalls) = %d, want 0: a value quoting an opener must not execute", len(resp.ToolCalls))
	}
	if !strings.Contains(resp.Content, "es la sintaxis") {
		t.Fatalf("the quoted text must survive, got %q", resp.Content)
	}
}

// TestRecoverDSMLToolCalls_ParameterValueQuotesParameter covers a value that
// quotes a complete <parameter> tag. It is markup inside a value, so the invoke
// is not executed: quoting a call must never run it.
func TestRecoverDSMLToolCalls_ParameterValueQuotesParameter(t *testing.T) {
	inner := "<" + fw + fw + "DSML" + fw + fw + "parameter name=\"name\" string=\"true\">x</" +
		fw + fw + "DSML" + fw + fw + "parameter>"
	value := "usa " + inner + " y listo"
	content := dsmlInvoke("exec",
		dsmlParam("command", value, true)+"\n"+dsmlParam("dir", "/tmp", true))

	for _, tc := range []struct {
		name    string
		content string
	}{
		{"bare", content},
		{"wrapper", dsmlCanonical(content)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: tc.content})
			if len(resp.ToolCalls) != 0 {
				t.Fatalf("len(ToolCalls) = %d, want 0: a value quoting a parameter must not execute", len(resp.ToolCalls))
			}
			if !strings.Contains(resp.Content, "y listo") {
				t.Fatalf("the quoted text must survive, got %q", resp.Content)
			}
		})
	}
}

// TestRecoverDSMLToolCalls_QuotedCallIsNotExecuted covers a value that quotes a
// complete call. This is the case that decides the design: a quoted call is
// byte-identical to a real nested one, so the only safe reading is "not
// executable". Executing it would run whatever the value happens to quote.
func TestRecoverDSMLToolCalls_QuotedCallIsNotExecuted(t *testing.T) {
	quoted := dsmlInvoke("exec", dsmlParam("command", "rm -rf /", true))
	content := dsmlInvoke("read", dsmlParam("q", "usa "+quoted+" y listo", true))

	resp, _ := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	for _, c := range resp.ToolCalls {
		if c.Name == "exec" {
			t.Fatalf("a quoted call must never be executed, got %v", c.Arguments)
		}
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("len(ToolCalls) = %d, want 0", len(resp.ToolCalls))
	}
}

// TestRecoverDSMLToolCalls_UnclosedParameterDoesNotStealSibling covers an
// unterminated opener whose <parameter> never closes, followed by a well-formed
// sibling.
//
// Nothing runs. The orphan must not run with the sibling's arguments, and the
// sibling itself is not recoverable either: after an unterminated parameter the
// markup that follows is byte-identical to a value quoting a complete call, and
// executing that would run whatever a value happens to quote. Losing one call
// is the price of never running an unopened one.
func TestRecoverDSMLToolCalls_UnclosedParameterDoesNotStealSibling(t *testing.T) {
	opener := func(n string) string {
		return "<" + fw + fw + "DSML" + fw + fw + "invoke name=\"" + n + "\">"
	}
	param := func(n string) string {
		return "<" + fw + fw + "DSML" + fw + fw + "parameter name=\"" + n + "\" string=\"true\">"
	}
	content := opener("orphan") + "\n" + param("x") + "sin cerrar\n" +
		dsmlInvoke("real", dsmlParam("command", "echo real", true))

	resp, _ := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	for _, c := range resp.ToolCalls {
		if c.Name == "orphan" {
			t.Fatalf("the orphan must not run, got %v", c.Arguments)
		}
	}
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("len(ToolCalls) = %d, want 0 (ambiguous markup must not execute)", len(resp.ToolCalls))
	}
	if !strings.Contains(resp.Content, "sin cerrar") {
		t.Fatalf("the prose must survive, got %q", resp.Content)
	}
}

// TestRecoverDSMLToolCalls_WrapperUnclosedInvokeBeforeValid covers a closed
// wrapper holding an unterminated invoke followed by a well-formed one. The
// valid invoke must be the one executed: taking the orphan would run a tool the
// model never closed and hand it the sibling's arguments.
func TestRecoverDSMLToolCalls_WrapperUnclosedInvokeBeforeValid(t *testing.T) {
	opener := func(n string) string {
		return "<" + fw + fw + "DSML" + fw + fw + "invoke name=\"" + n + "\">"
	}
	content := dsmlCanonical(
		opener("orphan") + "\nesto no lo cierro\n" +
			dsmlInvoke("real", dsmlParam("command", "echo real", true)),
	)

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok {
		t.Fatal("the well-formed invoke must be recovered")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "real" {
		t.Fatalf("name = %q, want 'real' (the orphan must not be executed)", resp.ToolCalls[0].Name)
	}
	if got := resp.ToolCalls[0].Arguments["command"]; got != "echo real" {
		t.Fatalf("command = %v, want 'echo real'", got)
	}
}

// TestRecoverDSMLToolCalls_UnclosedOpenerBetweenClosedSiblings covers a bare run
// where an unterminated opener sits between two well-formed invokes. Invoke
// elements are siblings, so the closed ones must both be recovered and the
// orphan must not borrow the arguments of the invoke that follows it.
func TestRecoverDSMLToolCalls_UnclosedOpenerBetweenClosedSiblings(t *testing.T) {
	opener := func(n string) string {
		return "<" + fw + fw + "DSML" + fw + fw + "invoke name=\"" + n + "\">"
	}
	content := opener("first") + "\n" + dsmlParam("command", "echo uno", true) + "\n" +
		"</" + fw + fw + "DSML" + fw + fw + "invoke>\n" +
		opener("orphan") + "\nno lo cierro\n" +
		opener("second") + "\n" + dsmlParam("command", "echo dos", true) + "\n" +
		"</" + fw + fw + "DSML" + fw + fw + "invoke>"

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok {
		t.Fatal("the two closed invokes must be recovered")
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("len(ToolCalls) = %d, want 2", len(resp.ToolCalls))
	}
	if got := resp.ToolCalls[0].Arguments["command"]; got != "echo uno" {
		t.Fatalf("first command = %v, want 'echo uno'", got)
	}
	if got := resp.ToolCalls[1].Arguments["command"]; got != "echo dos" {
		t.Fatalf("second command = %v, want 'echo dos'", got)
	}
	for _, c := range resp.ToolCalls {
		if c.Name == "orphan" {
			t.Fatalf("the unterminated opener must not be executed, got %v", c.Arguments)
		}
	}
	if !strings.Contains(resp.Content, "no lo cierro") {
		t.Fatalf("prose after the orphan must survive, got %q", resp.Content)
	}
}

// TestRecoverDSMLToolCalls_ParameterMentionsOpener covers a block whose
// parameter value quotes an opener literally (the model documenting its own
// syntax). Markup inside a value cannot be told apart from real markup, so the
// block is left as text rather than executing whatever it quotes.
func TestRecoverDSMLToolCalls_ParameterMentionsOpener(t *testing.T) {
	opener := "<" + fw + fw + "DSML" + fw + fw + "invoke name=\"exec\">"
	body := dsmlParam("command", "echo '"+opener+" es la sintaxis'", true)
	content := dsmlCanonical(dsmlInvoke("exec", body))

	resp, _ := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("len(ToolCalls) = %d, want 0: a value quoting an opener must not execute", len(resp.ToolCalls))
	}
	if !strings.Contains(resp.Content, "es la sintaxis") {
		t.Fatalf("the quoted text must survive, got %q", resp.Content)
	}
}

// TestRecoverDSMLToolCalls_ProseMentionsOpener covers a wrapper whose prose
// mentions an opener of the same element before the real invoke.
func TestRecoverDSMLToolCalls_ProseMentionsOpener(t *testing.T) {
	opener := "<" + fw + fw + "DSML" + fw + fw + "invoke name=\"exec\">"
	content := "<" + fw + fw + "DSML" + fw + fw + "tool_calls>\n" +
		"Nota: el formato es " + opener + " y luego el parametro.\n" +
		dsmlInvoke("exec", dsmlParam("command", "echo hola", true)) + "\n" +
		"</" + fw + fw + "DSML" + fw + fw + "tool_calls>"

	resp, ok := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if !ok {
		t.Fatal("prose mentioning an opener must not hide the real invoke")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(resp.ToolCalls))
	}
}

// TestRecoverDSMLToolCalls_UnclosedOpenersStayLinear guards the scan against
// quadratic behaviour: content full of unclosed openers is model output and
// must not be able to stall the turn (1.18 MB used to take ~55s).
//
// The check is a ratio, not an absolute deadline: a wall-clock threshold is
// flaky on a loaded CI box and would not notice a superlinear curve that stays
// under it. Quadrupling the input must not multiply the time by more than a
// generous constant; a quadratic scan lands around 16x.
func TestRecoverDSMLToolCalls_UnclosedOpenersStayLinear(t *testing.T) {
	opener := "<" + fw + fw + "DSML" + fw + fw + "invoke name=\"exec\">"
	if testing.Short() {
		t.Skip("timing test")
	}
	measure := func(n int) time.Duration {
		content := strings.Repeat(opener, n) + "tail"
		start := time.Now()
		RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
		return time.Since(start)
	}
	measure(250) // warm up
	small := measure(4000)
	large := measure(16000)
	if small < time.Millisecond {
		small = time.Millisecond // avoid dividing by a timer-resolution zero
	}
	if ratio := float64(large) / float64(small); ratio > 8 {
		t.Fatalf("4x the input took %.1fx the time (%v -> %v), want linear", ratio, small, large)
	}
}

// TestRecoverDSMLToolCalls_ValidSiblingsAroundOrphan pins what survives an
// unterminated parameter: invokes that closed before it are still recovered, and
// the ones after it are not (their markup is indistinguishable from a value
// quoting a complete call).
func TestRecoverDSMLToolCalls_ValidSiblingsAroundOrphan(t *testing.T) {
	opener := func(n string) string {
		return "<" + fw + fw + "DSML" + fw + fw + "invoke name=\"" + n + "\">"
	}
	param := func(n string) string {
		return "<" + fw + fw + "DSML" + fw + fw + "parameter name=\"" + n + "\" string=\"true\">"
	}
	before := dsmlInvoke("first", dsmlParam("command", "echo uno", true))
	after := dsmlInvoke("third", dsmlParam("command", "echo tres", true))
	content := before + "\n" +
		opener("orphan") + "\n" + param("x") + "sin cerrar\n" +
		after

	resp, _ := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1 (only the invoke closed before the orphan)", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "first" {
		t.Fatalf("name = %q, want 'first'", resp.ToolCalls[0].Name)
	}
	if got := resp.ToolCalls[0].Arguments["command"]; got != "echo uno" {
		t.Fatalf("command = %v, want 'echo uno'", got)
	}
}

// TestRecoverDSMLToolCalls_DeepNestingInsideAbandonedValueIsNotExecuted pins the
// taint propagation: once a parameter is abandoned, everything opened inside it
// is untrusted however deep and however well-formed, because its markup came
// from inside a value that never closed.
func TestRecoverDSMLToolCalls_DeepNestingInsideAbandonedValueIsNotExecuted(t *testing.T) {
	opener := "<" + fw + fw + "DSML" + fw + fw + "invoke name=\"x\">"
	param := "<" + fw + fw + "DSML" + fw + fw + "parameter name=\"p\" string=\"true\">"
	closer := "</" + fw + fw + "DSML" + fw + fw + "invoke>"

	// An abandoned parameter, then a deep, perfectly well-formed call inside it.
	deep := strings.Repeat(opener, 8) + dsmlParam("command", "rm -rf /", true) +
		strings.Repeat(closer, 8)
	content := opener + param + "sin cerrar\n" + deep

	resp, _ := RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
	if len(resp.ToolCalls) != 0 {
		t.Fatalf("len(ToolCalls) = %d, want 0: markup inside an abandoned value must not run", len(resp.ToolCalls))
	}
	if !strings.Contains(resp.Content, "rm -rf /") {
		t.Fatalf("the value must survive as text, got %q", resp.Content)
	}
}

// TestRecoverDSMLToolCalls_UnclosedParametersStayLinear covers the nastier
// shape: every opener is followed by a <parameter> that never closes. Skipping
// a parameter by searching for its close would scan the rest of the content for
// each opener (8x the input, 53x the time); the single-pass state machine walks
// it once.
func TestRecoverDSMLToolCalls_UnclosedParametersStayLinear(t *testing.T) {
	unit := "<" + fw + fw + "DSML" + fw + fw + "invoke name=\"exec\">\n" +
		"<" + fw + fw + "DSML" + fw + fw + "parameter name=\"x\" string=\"true\">"
	if testing.Short() {
		t.Skip("timing test")
	}
	measure := func(n int) time.Duration {
		content := strings.Repeat(unit, n) + "tail"
		start := time.Now()
		RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
		return time.Since(start)
	}
	measure(250) // warm up
	small := measure(1000)
	large := measure(4000)
	if small < time.Millisecond {
		small = time.Millisecond
	}
	if ratio := float64(large) / float64(small); ratio > 8 {
		t.Fatalf("4x the input took %.1fx the time (%v -> %v), want linear", ratio, small, large)
	}
}

// BenchmarkRecoverDSMLToolCalls_UnclosedOpeners keeps the linearity claim
// measurable without a wall-clock assertion.
func BenchmarkRecoverDSMLToolCalls_UnclosedOpeners(b *testing.B) {
	opener := "<" + fw + fw + "DSML" + fw + fw + "invoke name=\"exec\">"
	for _, n := range []int{1000, 4000, 16000} {
		content := strings.Repeat(opener, n) + "tail"
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
			}
		})
	}
}
