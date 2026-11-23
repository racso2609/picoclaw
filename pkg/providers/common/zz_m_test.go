package common

import (
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

func TestZZMeasure(t *testing.T) {
	m := fw + fw + "DSML" + fw + fw
	opener := "<" + m + "invoke name=\"x\">"
	param := "<" + m + "parameter name=\"p\">"
	for _, d := range []int{2000, 4000, 8000, 16000, 32000} {
		content := opener + param + strings.Repeat(opener, d) + "tail"
		st := time.Now()
		RecoverDSMLToolCalls(&protocoltypes.LLMResponse{Content: content})
		t.Logf("d=%-6d %v", d, time.Since(st))
	}
}
