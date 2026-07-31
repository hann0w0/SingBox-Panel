package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/singpanel/singpanel/internal/protocol"
)

func TestOnCommandRejectsInvalidPayload(t *testing.T) {
	a := &Agent{}
	res := a.onCommand(context.Background(), protocol.Envelope{
		Type:    protocol.CmdTestOutbound,
		ID:      "bad-payload",
		Payload: []byte(`{"host":`),
	})
	if res.OK || !strings.Contains(res.Error, "decode command payload") {
		t.Fatalf("result = %+v", res)
	}
}

func TestEveryCommandHasABoundedTimeout(t *testing.T) {
	for _, command := range []protocol.MessageType{
		protocol.CmdInstallSingbox,
		protocol.CmdApplyConfig,
		protocol.CmdServiceAction,
		protocol.CmdGetStatus,
		protocol.CmdGetConfig,
		protocol.CmdUpdateAgent,
		protocol.CmdTestOutbound,
		protocol.CmdTestEgress,
		protocol.CmdGetLogs,
		protocol.CmdUninstallAgent,
	} {
		if timeout := commandTimeout(command); timeout <= 0 {
			t.Fatalf("command %s timeout = %s", command, timeout)
		}
	}
}
