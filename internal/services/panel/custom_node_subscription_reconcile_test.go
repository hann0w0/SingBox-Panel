package panel

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
	"gorm.io/gorm"
)

type subscriptionReconcileFixture struct {
	db      *gorm.DB
	manager *customNodeSubscriptionManager
	source  model.CustomNodeSubscription
	whole   model.User
	partial model.User
	body    string
}

func newSubscriptionReconcileFixture(t *testing.T, entries ...string) *subscriptionReconcileFixture {
	t.Helper()
	f := &subscriptionReconcileFixture{db: testDB(t)}
	f.source = model.CustomNodeSubscription{
		Name: "synthetic source", URL: "https://example.com/subscription", Enabled: true,
		NameRewriteRules: []model.NameRewriteRule{{Pattern: "source-", Replacement: "shown-"}},
	}
	f.whole = model.User{Email: "whole@example.com", SubToken: "whole-test-token"}
	f.partial = model.User{Email: "partial@example.com", SubToken: "partial-test-token"}
	for _, row := range []any{&f.source, &f.whole, &f.partial} {
		if err := f.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	f.manager = newCustomNodeSubscriptionManager(f.db)
	f.manager.fetch = func(_ context.Context, _ *url.URL) ([]byte, error) {
		return []byte(f.body), nil
	}
	f.body = subscriptionReconcileYAML(entries...)
	if _, err := f.manager.sync(context.Background(), f.source.ID); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	for _, row := range f.nodes(t) {
		row.UserIDs = []uint{f.whole.ID}
		if err := f.db.Save(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func subscriptionReconcileProxy(name, address string) string {
	return fmt.Sprintf("  - {name: %q, type: trojan, server: %q, port: 443, password: test-only-password}", name, address)
}

func subscriptionReconcileInvalid(name string) string {
	return fmt.Sprintf("  - {name: %q, type: trojan, server: broken.example.com, port: 0, password: test-only-password}", name)
}

func subscriptionReconcileYAML(entries ...string) string {
	return "proxies:\n" + strings.Join(entries, "\n") + "\n"
}

func (f *subscriptionReconcileFixture) nodes(t *testing.T) []model.CustomNode {
	t.Helper()
	var rows []model.CustomNode
	if err := f.db.Where("subscription_id = ?", f.source.ID).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func (f *subscriptionReconcileFixture) order(t *testing.T) []model.UserNodeOrder {
	t.Helper()
	var rows []model.UserNodeOrder
	if err := f.db.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func (f *subscriptionReconcileFixture) grantPartialAndOrder(t *testing.T, row model.CustomNode) {
	t.Helper()
	row.UserIDs = []uint{f.whole.ID, f.partial.ID}
	if err := f.db.Save(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.UserNodeOrder{
		UserID: f.partial.ID, NodeType: userNodeTypeCustom, NodeID: row.ID,
		SortOrder: 9, CustomName: "my selected node",
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestSubscriptionSyncNamedFailureCleansStaleNodes(t *testing.T) {
	var stable []string
	for i := 0; i < 26; i++ {
		stable = append(stable, subscriptionReconcileProxy(fmt.Sprintf("source-stable-%02d", i), fmt.Sprintf("stable-%02d.example.com", i)))
	}
	initial := append(append([]string{}, stable...),
		subscriptionReconcileProxy("source-broken", "broken.example.com"),
		subscriptionReconcileProxy("source-renamed-0", "rotating.example.com"),
		subscriptionReconcileProxy("source-removed", "removed.example.com"),
	)
	f := newSubscriptionReconcileFixture(t, initial...)
	var broken, removed model.CustomNode
	for _, row := range f.nodes(t) {
		switch row.SourceName {
		case "source-broken":
			broken = row
		case "source-removed":
			removed = row
		}
	}
	if broken.ID == 0 || removed.ID == 0 {
		t.Fatal("initial nodes missing")
	}
	f.grantPartialAndOrder(t, broken)
	if err := f.db.Model(&broken).Updates(map[string]any{"user_enabled": false, "enabled": false}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.First(&broken, broken.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&model.UserNodeOrder{
		UserID: f.whole.ID, NodeType: userNodeTypeCustom, NodeID: removed.ID, SortOrder: 10,
	}).Error; err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 3; round++ {
		entries := append(append([]string{}, stable...),
			subscriptionReconcileProxy(fmt.Sprintf("source-renamed-%d", round), "rotating.example.com"),
			subscriptionReconcileProxy("source-added", "added.example.com"),
			subscriptionReconcileInvalid("source-broken"),
		)
		// Identical provider duplicates do not create independently assignable rows.
		entries = append(entries, entries[0])
		f.body = subscriptionReconcileYAML(entries...)
		result, err := f.manager.sync(context.Background(), f.source.ID)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		rows := f.nodes(t)
		if len(rows) != 29 || result.Total != 29 || result.Skipped != 1 {
			t.Fatalf("round %d grew stale rows: stored=%d result=%+v, want 28 valid + 1 retained", round, len(rows), result)
		}
		keptBroken := false
		for _, row := range rows {
			if row.SourceName == "source-removed" || strings.HasPrefix(row.SourceName, "source-renamed-") && row.SourceName != fmt.Sprintf("source-renamed-%d", round) {
				t.Fatalf("round %d retained deleted/renamed source node %q", round, row.SourceName)
			}
			if row.ID == broken.ID {
				keptBroken = true
				if !reflect.DeepEqual(row, broken) {
					t.Fatalf("failed node lost its saved definition, grants, switch or order: got %+v want %+v", row, broken)
				}
			}
			if row.SourceName == "source-added" || row.SourceName == fmt.Sprintf("source-renamed-%d", round) {
				if !row.HasUser(f.whole.ID) || row.HasUser(f.partial.ID) {
					t.Fatalf("new node %q inherited wrong audience: %v", row.SourceName, row.UserIDs)
				}
			}
		}
		if !keptBroken {
			t.Fatal("failed source node lost its existing row")
		}
		orders := f.order(t)
		if len(orders) != 1 || orders[0].NodeID != broken.ID || orders[0].SortOrder != 9 || orders[0].CustomName != "my selected node" {
			t.Fatalf("retained/deleted node order references = %+v", orders)
		}
		var source model.CustomNodeSubscription
		if err := f.db.First(&source, f.source.ID).Error; err != nil {
			t.Fatal(err)
		}
		if source.NodeCount != 29 || source.LastError == "" {
			t.Fatalf("partial sync status = %+v", source)
		}
	}
}

func TestSubscriptionSyncCleansAccumulatedRows(t *testing.T) {
	var current []string
	for i := 0; i < 28; i++ {
		current = append(current, subscriptionReconcileProxy(fmt.Sprintf("source-current-%02d", i), fmt.Sprintf("current-%02d.example.com", i)))
	}
	initial := append(append([]string{}, current...), subscriptionReconcileProxy("source-broken", "broken.example.com"))
	for i := 0; i < 52; i++ {
		initial = append(initial, subscriptionReconcileProxy(fmt.Sprintf("source-expired-%02d", i), fmt.Sprintf("expired-%02d.example.com", i)))
	}
	f := newSubscriptionReconcileFixture(t, initial...)
	var broken model.CustomNode
	for _, row := range f.nodes(t) {
		if row.SourceName == "source-broken" {
			broken = row
		}
	}
	if len(f.nodes(t)) != 81 || broken.ID == 0 {
		t.Fatal("expected 81 accumulated rows including the failed source node")
	}
	f.body = subscriptionReconcileYAML(append(current, subscriptionReconcileInvalid("source-broken"))...)
	for round := 0; round < 2; round++ {
		result, err := f.manager.sync(context.Background(), f.source.ID)
		if err != nil {
			t.Fatal(err)
		}
		wantDeleted := 52 * (1 - round)
		if result.Updated != 28 || result.Created != 0 || result.Deleted != wantDeleted || result.Total != 29 || len(f.nodes(t)) != 29 {
			t.Fatalf("round %d did not converge 81 accumulated rows to 29: %+v", round, result)
		}
		var retained model.CustomNode
		if err := f.db.First(&retained, broken.ID).Error; err != nil || !reflect.DeepEqual(retained, broken) {
			t.Fatalf("failed source node was not retained unchanged: %+v, %v", retained, err)
		}
	}
}

func TestSubscriptionSyncUnknownFailureIsAtomic(t *testing.T) {
	for _, scenario := range []string{"non-object", "unnamed", "node-limit"} {
		t.Run(scenario, func(t *testing.T) {
			f := newSubscriptionReconcileFixture(t,
				subscriptionReconcileProxy("source-kept", "kept.example.com"),
				subscriptionReconcileProxy("source-removed", "removed.example.com"),
			)
			f.grantPartialAndOrder(t, f.nodes(t)[0])
			beforeNodes, beforeOrder := f.nodes(t), f.order(t)
			var beforeSource model.CustomNodeSubscription
			if err := f.db.First(&beforeSource, f.source.ID).Error; err != nil {
				t.Fatal(err)
			}
			entries := []string{
				subscriptionReconcileProxy("source-kept", "rotated.example.com"),
				subscriptionReconcileProxy("source-added", "added.example.com"),
			}
			switch scenario {
			case "non-object":
				entries = append(entries, "  - malformed-node")
			case "unnamed":
				entries = append(entries, subscriptionReconcileInvalid(""))
			case "node-limit":
				for i := len(entries); i <= singbox.SubscriptionImportMaxNodes; i++ {
					entries = append(entries, subscriptionReconcileProxy(fmt.Sprintf("source-extra-%03d", i), fmt.Sprintf("extra-%03d.example.com", i)))
				}
			}
			f.body = subscriptionReconcileYAML(entries...)
			if _, err := f.manager.sync(context.Background(), f.source.ID); err == nil {
				t.Fatal("unidentifiable/truncated source should fail before reconciling")
			}
			if after := f.nodes(t); !reflect.DeepEqual(after, beforeNodes) {
				t.Fatalf("failed sync changed nodes or grants: before=%+v after=%+v", beforeNodes, after)
			}
			if after := f.order(t); !reflect.DeepEqual(after, beforeOrder) {
				t.Fatalf("failed sync changed order references: before=%+v after=%+v", beforeOrder, after)
			}
			var source model.CustomNodeSubscription
			if err := f.db.First(&source, f.source.ID).Error; err != nil {
				t.Fatal(err)
			}
			if source.NodeCount != beforeSource.NodeCount || !reflect.DeepEqual(source.LastSuccessAt, beforeSource.LastSuccessAt) || source.LastError == "" {
				t.Fatalf("failed sync changed successful result metadata: before=%+v after=%+v", beforeSource, source)
			}
		})
	}
}

func TestSubscriptionSyncSameNameFailureKeepsAssignments(t *testing.T) {
	f := newSubscriptionReconcileFixture(t,
		subscriptionReconcileProxy("source-same", "a.example.com"),
		subscriptionReconcileProxy("source-same", "b.example.com"),
		subscriptionReconcileProxy("source-stale", "stale.example.com"),
	)
	var a, b model.CustomNode
	for _, row := range f.nodes(t) {
		if row.Address == "a.example.com" {
			a = row
		} else if row.Address == "b.example.com" {
			b = row
		}
	}
	f.grantPartialAndOrder(t, a)
	if err := f.db.First(&a, a.ID).Error; err != nil {
		t.Fatal(err)
	}
	f.body = subscriptionReconcileYAML(
		subscriptionReconcileProxy("source-same", "b.example.com"),
		subscriptionReconcileInvalid("source-same"),
		subscriptionReconcileProxy("source-new", "new.example.com"),
	)
	result, err := f.manager.sync(context.Background(), f.source.ID)
	if err != nil {
		t.Fatal(err)
	}
	rows := f.nodes(t)
	if len(rows) != 3 || result.Deleted != 1 {
		t.Fatalf("ambiguous name blocked unrelated cleanup: nodes=%d result=%+v", len(rows), result)
	}
	var keptA, keptB bool
	for _, row := range rows {
		switch row.ID {
		case a.ID:
			keptA = true
			if !reflect.DeepEqual(row, a) {
				t.Fatalf("ambiguous failed node changed: got %+v want %+v", row, a)
			}
		case b.ID:
			keptB = true
			if row.Address != b.Address || !reflect.DeepEqual(row.UserIDs, b.UserIDs) {
				t.Fatalf("successful same-name node inherited another endpoint's assignment: %+v", row)
			}
		}
	}
	if !keptA || !keptB {
		t.Fatalf("same-name row identities lost: a=%v b=%v", keptA, keptB)
	}
}

func TestSubscriptionSyncNamedURIFailures(t *testing.T) {
	const uuid = "12345678-1234-1234-1234-123456789012"
	for _, encoded := range []bool{false, true} {
		t.Run(fmt.Sprintf("base64=%v", encoded), func(t *testing.T) {
			var initial, links []string
			for i := 0; i < 17; i++ {
				name, address := fmt.Sprintf("source-valid-%02d", i), fmt.Sprintf("valid-%02d.example.com", i)
				if i < 16 {
					initial = append(initial, fmt.Sprintf("  - {name: %q, type: vless, server: %q, port: 443, uuid: %q, tls: true}", name, address, uuid))
				}
				links = append(links, fmt.Sprintf("vless://%s@%s:443?security=tls&type=tcp#%s", uuid, address, url.PathEscape(name)))
			}
			for i := 0; i < 14; i++ {
				name := fmt.Sprintf("source-xhttp %02d + 亚洲", i)
				initial = append(initial, subscriptionReconcileProxy(name, fmt.Sprintf("previous-%02d.example.com", i)))
				links = append(links, fmt.Sprintf("vless://%s@xhttp-%02d.example.com:443?security=tls&type=xhttp#%s", uuid, i, url.PathEscape(name)))
			}
			initial = append(initial, subscriptionReconcileProxy("source-stale", "stale.example.com"))
			f := newSubscriptionReconcileFixture(t, initial...)
			var failed model.CustomNode
			for _, row := range f.nodes(t) {
				if row.SourceName == "source-xhttp 00 + 亚洲" {
					failed = row
				}
			}
			f.grantPartialAndOrder(t, failed)
			if err := f.db.First(&failed, failed.ID).Error; err != nil {
				t.Fatal(err)
			}
			f.body = strings.Join(links, "\n")
			if encoded {
				f.body = base64.StdEncoding.EncodeToString([]byte(f.body))
			}
			for round := 0; round < 2; round++ {
				result, err := f.manager.sync(context.Background(), f.source.ID)
				if err != nil {
					t.Fatalf("31 named URI entries should partially sync: %v", err)
				}
				if result.Created != 1-round || result.Updated != 16+round || result.Deleted != 1-round || result.Total != 31 || result.Skipped != 14 {
					t.Fatalf("round %d URI reconciliation = %+v", round, result)
				}
				rows := f.nodes(t)
				if len(rows) != 31 {
					t.Fatalf("round %d stored %d nodes, want 17 valid + 14 retained", round, len(rows))
				}
				for _, row := range rows {
					if row.SourceName == "source-stale" {
						t.Fatal("unrelated deleted node was retained")
					}
					if row.SourceName == "source-valid-16" && (!row.HasUser(f.whole.ID) || row.HasUser(f.partial.ID)) {
						t.Fatalf("new valid URI node inherited wrong grants: %v", row.UserIDs)
					}
				}
				var retained model.CustomNode
				if err := f.db.First(&retained, failed.ID).Error; err != nil || !reflect.DeepEqual(retained, failed) {
					t.Fatalf("named xhttp failure lost saved node or audience: %+v, %v", retained, err)
				}
				if orders := f.order(t); len(orders) != 1 || orders[0].NodeID != failed.ID || orders[0].CustomName != "my selected node" {
					t.Fatalf("failed URI node lost user order: %+v", orders)
				}
			}
		})
	}
}

func TestSubscriptionSyncUnnamedURIFailureIsAtomic(t *testing.T) {
	const valid = "vless://12345678-1234-1234-1234-123456789012@valid.example.com:443?security=tls&type=tcp#source-added"
	for _, encoded := range []bool{false, true} {
		t.Run(fmt.Sprintf("base64=%v", encoded), func(t *testing.T) {
			f := newSubscriptionReconcileFixture(t, subscriptionReconcileProxy("source-old", "old.example.com"))
			f.grantPartialAndOrder(t, f.nodes(t)[0])
			beforeNodes, beforeOrder := f.nodes(t), f.order(t)
			f.body = valid + "\nvless://broken\n"
			if encoded {
				f.body = base64.StdEncoding.EncodeToString([]byte(f.body))
			}
			if _, err := f.manager.sync(context.Background(), f.source.ID); err == nil {
				t.Fatal("unnamed damaged URI should reject the entire refresh")
			}
			if !reflect.DeepEqual(f.nodes(t), beforeNodes) || !reflect.DeepEqual(f.order(t), beforeOrder) {
				t.Fatal("unnamed damaged URI changed saved nodes, grants or user order")
			}
		})
	}
}
