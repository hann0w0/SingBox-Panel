package panel

import (
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func TestSubscriptionIdentityPreservesDuplicateNodeAudiences(t *testing.T) {
	source := model.CustomNodeSubscription{ID: 1, Enabled: true}
	makeDesired := func(addresses ...string) (map[string]model.CustomNode, []string) {
		t.Helper()
		rows := make(map[string]model.CustomNode)
		var order []string
		for i, address := range addresses {
			row, key, err := subscriptionCustomNode(source, singbox.ImportedNode{
				Name: "same-name", Protocol: "trojan", Address: address, Port: 443,
				Params: map[string]any{"password": address, "tls": "tls"},
			}, i, i, nil)
			if err != nil {
				t.Fatal(err)
			}
			rows[key] = row
			order = append(order, key)
		}
		return rows, order
	}
	initial, keys := makeDesired("a.example.com", "b.example.com")
	a, b := initial[keys[0]], initial[keys[1]]
	a.ID, a.UserIDs = 10, []uint{1}
	b.ID, b.UserIDs = 20, []uint{2}
	existing := []model.CustomNode{a, b}
	for _, scenario := range []struct {
		name      string
		addresses []string
	}{
		{"reordered", []string{"b.example.com", "a.example.com"}},
		{"first removed", []string{"b.example.com"}},
		{"new first", []string{"c.example.com", "b.example.com", "a.example.com"}},
		{"rotated duplicate", []string{"a-new.example.com", "b.example.com"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			desired, order := makeDesired(scenario.addresses...)
			matched, matchedOrder, err := matchSubscriptionNodeKeys(existing, desired, order)
			if err != nil {
				t.Fatal(err)
			}
			if len(matched) != len(scenario.addresses) || len(matchedOrder) != len(scenario.addresses) {
				t.Fatal("nodes were lost during identity matching")
			}
			for i, key := range matchedOrder {
				row := matched[key]
				if row.Address != scenario.addresses[i] || row.SortOrder != i {
					t.Fatal("provider display order was not retained")
				}
				for _, old := range existing {
					if row.Address == old.Address && key != old.SubscriptionKey {
						t.Fatalf("%s lost its assigned row %d", row.Address, old.ID)
					}
					if key == old.SubscriptionKey && row.Address != old.Address {
						t.Fatalf("%s inherited %s's user audience %v", row.Address, old.Address, old.UserIDs)
					}
				}
			}
		})
	}
	// Unique names still preserve intentional endpoint/credential rotations.
	rotated, rotatedOrder := makeDesired("rotated.example.com")
	matched, _, err := matchSubscriptionNodeKeys([]model.CustomNode{b}, rotated, rotatedOrder)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := matched[b.SubscriptionKey]; !ok || got.Address != "rotated.example.com" {
		t.Fatal("unique-name endpoint rotation lost its existing row")
	}
}

func TestSubscriptionIdentityRecoversLegacyURIEdit(t *testing.T) {
	parsed, err := singbox.ParseSubscription([]byte("socks5://alice:secret@edge.example.com:1080#same-name\nsocks5://alice:secret@edge.example.com:2080#same-name"))
	if err != nil || len(parsed.Nodes) != 2 {
		t.Fatalf("parse URI nodes: %v, %+v", err, parsed)
	}
	source := model.CustomNodeSubscription{ID: 1, Enabled: true}
	desired := make(map[string]model.CustomNode)
	order := make([]string, 0, 2)
	for index, item := range parsed.Nodes {
		row, key, err := subscriptionCustomNode(source, item, index, index, nil)
		if err != nil {
			t.Fatal(err)
		}
		desired[key] = row
		order = append(order, key)
	}
	legacy := desired[order[0]]
	legacy.ID, legacy.UserIDs = 42, []uint{7}
	legacy.Protocol, legacy.Address, legacy.Port, legacy.Params = "", "", 0, nil
	second := desired[order[1]]
	second.ID, second.UserIDs = 43, []uint{8}
	matched, matchedOrder, err := matchSubscriptionNodeKeys([]model.CustomNode{legacy, second}, desired, []string{order[1], order[0]})
	if err != nil {
		t.Fatal(err)
	}
	if len(matchedOrder) != 2 || matchedOrder[0] != second.SubscriptionKey || matchedOrder[1] != legacy.SubscriptionKey {
		t.Fatalf("URI edit or reorder transferred a grant: got %v, want %v", matchedOrder, []string{second.SubscriptionKey, legacy.SubscriptionKey})
	}
	if matched[legacy.SubscriptionKey].Port != 1080 || matched[second.SubscriptionKey].Port != 2080 {
		t.Fatalf("same-name endpoints were swapped: %+v", matched)
	}
}
