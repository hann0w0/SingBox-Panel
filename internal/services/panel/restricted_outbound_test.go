package panel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func TestPrivateOutboundCommandsRequireNodeValidationCapability(t *testing.T) {
	fixture := newForwardFixture(t)
	fixture.create(t)
	raw, err := fixture.app.orch.BuildServerConfig(&fixture.server)
	if err != nil {
		t.Fatal(err)
	}
	command, err := fixture.app.orch.applyConfigCommand(raw)
	if err != nil || len(command.RestrictedOutboundTags) != 1 || command.RestrictedOutboundTags[0] != fixture.outbound.Tag {
		t.Fatalf("private restriction missing: %+v %v", command, err)
	}
	hub := fixture.app.orch.hub
	connection := hub.register(fixture.server.ID, "", nil)
	t.Cleanup(connection.close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := hub.SendCommand(ctx, fixture.server.ID, protocol.CmdApplyConfig, command); err == nil || !strings.Contains(err.Error(), "Agent") {
		t.Fatalf("old Agent accepted restricted config: %v", err)
	}
	if len(connection.send) != 0 {
		t.Fatal("unsafe config reached old Agent")
	}
	hub.onRegister(connection, protocol.RegisterEvt{RestrictedOutboundValidation: true})
	result := make(chan error, 1)
	go func() {
		_, err := hub.SendCommand(ctx, fixture.server.ID, protocol.CmdApplyConfig, command)
		result <- err
	}()
	select {
	case envelope := <-connection.send:
		var sent protocol.ApplyConfigCmd
		if err := json.Unmarshal(envelope.Payload, &sent); err != nil || len(sent.RestrictedOutboundTags) != 1 {
			t.Fatalf("wire restriction missing: %+v %v", sent, err)
		}
		replySubscriptionConfig(connection, envelope, true)
	case <-ctx.Done():
		t.Fatal("capable Agent did not receive command")
	}
	if err := <-result; err != nil {
		t.Fatalf("capable Agent rejected: %v", err)
	}
}
