package panel

import (
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestGatherNodesUsesPerUserMixedOrder(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "managed", AgentToken: "order-agent", Address: "managed.example.com"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	managedA := model.Inbound{ServerID: server.ID, Tag: "managed-a", Type: model.InboundVLESS, ListenPort: 10001, Enabled: true}
	managedB := model.Inbound{ServerID: server.ID, Tag: "managed-b", Type: model.InboundTrojan, ListenPort: 10002, Enabled: true}
	if err := db.Create(&managedA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&managedB).Error; err != nil {
		t.Fatal(err)
	}
	user := model.User{
		Email: "ordered-user", Password: "unused", Role: model.RoleUser, Enabled: true,
		ServerIDs: []uint{server.ID}, InboundIDs: []uint{managedA.ID, managedB.ID}, SubToken: "ordered-token",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	custom := model.CustomNode{
		Name: "custom-node", Link: "socks5://127.0.0.1:1080#custom-node",
		UserIDs: []uint{user.ID}, SortOrder: 0,
	}
	if err := db.Create(&custom).Error; err != nil {
		t.Fatal(err)
	}
	order := []model.UserNodeOrder{
		{UserID: user.ID, NodeType: userNodeTypeCustom, NodeID: custom.ID, SortOrder: 0},
		{UserID: user.ID, NodeType: userNodeTypeManaged, NodeID: managedB.ID, SortOrder: 1},
		{UserID: user.ID, NodeType: userNodeTypeManaged, NodeID: managedA.ID, SortOrder: 2},
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}

	app := &App{db: db}
	nodes := app.gatherNodes(&user)
	if len(nodes) != 3 {
		t.Fatalf("gathered %d nodes, want 3", len(nodes))
	}
	if nodes[0].orderType != userNodeTypeCustom || nodes[0].orderID != custom.ID ||
		nodes[1].orderType != userNodeTypeManaged || nodes[1].orderID != managedB.ID ||
		nodes[2].orderType != userNodeTypeManaged || nodes[2].orderID != managedA.ID {
		t.Fatalf("gathered order = [%s:%d %s:%d %s:%d]", nodes[0].orderType, nodes[0].orderID,
			nodes[1].orderType, nodes[1].orderID, nodes[2].orderType, nodes[2].orderID)
	}
	if nodes[2].name != "managed - managed-a" {
		t.Fatalf("managed node name = %q; want original generated name", nodes[2].name)
	}
	if nodes[0].name != "custom-node" {
		t.Fatalf("custom node name changed = %q", nodes[0].name)
	}
}

func TestOrderedUserNodeOrderAppendsNewAssignments(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "append", AgentToken: "append-agent"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	a := model.Inbound{ServerID: server.ID, Tag: "a", Type: model.InboundVLESS, ListenPort: 11001}
	b := model.Inbound{ServerID: server.ID, Tag: "b", Type: model.InboundVLESS, ListenPort: 11002}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&b).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.UserNodeOrder{UserID: 9, NodeType: userNodeTypeManaged, NodeID: b.ID, SortOrder: 0}).Error; err != nil {
		t.Fatal(err)
	}
	order, err := orderedUserNodeOrder(db, 9, []uint{a.ID, b.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0].NodeID != b.ID || order[1].NodeID != a.ID {
		t.Fatalf("order = %+v", order)
	}
}

func TestPerUserCustomNodeNameOverridesManagedAndSingleNodeDefaults(t *testing.T) {
	db := testDB(t)
	server := model.Server{Name: "🇭🇰 香港主节点", AgentToken: "cn-agent", Address: "hk.example.com", Region: "HK"}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	ib := model.Inbound{ServerID: server.ID, Tag: "vless-in", Type: model.InboundVLESS, ListenPort: 21001, Enabled: true}
	if err := db.Create(&ib).Error; err != nil {
		t.Fatal(err)
	}

	userA := model.User{
		Email: "alice", Password: "unused", Role: model.RoleUser, Enabled: true,
		ServerIDs: []uint{server.ID}, InboundIDs: []uint{ib.ID}, SubToken: "alice-sub",
	}
	userB := model.User{
		Email: "bob", Password: "unused", Role: model.RoleUser, Enabled: true,
		ServerIDs: []uint{server.ID}, InboundIDs: []uint{ib.ID}, SubToken: "bob-sub",
	}
	if err := db.Create(&userA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&userB).Error; err != nil {
		t.Fatal(err)
	}

	// Alice gets a custom display name on her single assigned node; Bob keeps the default.
	order := []userNodeOrderItem{
		{NodeType: userNodeTypeManaged, NodeID: ib.ID, CustomName: "  🇯🇵 专属专线 01  "},
	}
	if err := validateUserNodeOrder(order, []uint{ib.ID}, nil); err != nil {
		t.Fatalf("validateUserNodeOrder: %v", err)
	}
	if err := saveUserNodeOrder(db, userA.ID, order); err != nil {
		t.Fatal(err)
	}

	loaded, err := orderedUserNodeOrder(db, userA.ID, []uint{ib.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].CustomName != "🇯🇵 专属专线 01" {
		t.Fatalf("orderedUserNodeOrder = %+v", loaded)
	}

	app := &App{db: db}
	aliceNodes := app.gatherNodes(&userA)
	if len(aliceNodes) != 1 || aliceNodes[0].name != "🇯🇵 专属专线 01" || aliceNodes[0].region != "JP" {
		t.Fatalf("alice nodes = %+v", aliceNodes)
	}
	bobNodes := app.gatherNodes(&userB)
	if len(bobNodes) != 1 || bobNodes[0].name != "🇭🇰 香港主节点" || bobNodes[0].region != "HK" {
		t.Fatalf("bob nodes = %+v", bobNodes)
	}

	// Reject control characters in custom names.
	badOrder := []userNodeOrderItem{
		{NodeType: userNodeTypeManaged, NodeID: ib.ID, CustomName: "bad\nname"},
	}
	if err := validateUserNodeOrder(badOrder, []uint{ib.ID}, nil); err == nil {
		t.Fatal("expected validateUserNodeOrder to reject newline in custom_name")
	}
}
