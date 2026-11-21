package common

import (
	"strings"
	"testing"

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
