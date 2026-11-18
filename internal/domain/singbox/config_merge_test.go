package singbox

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestMergeConfigJSONPreservesUnchangedAndUnknownFields(t *testing.T) {
	original := []byte(`{"port":443,"tls":{"enabled":true,"custom":{"option":42},"server_name":"old"},"extra":9007199254740993}`)
	before := []byte(`{"port":443,"tls":{"enabled":true,"server_name":"old"}}`)
	after := []byte(`{"port":8443,"tls":{"enabled":true,"server_name":"new"}}`)
	got, err := mergeConfigJSON(original, before, after)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"port":8443,"tls":{"enabled":true,"custom":{"option":42},"server_name":"new"},"extra":9007199254740993}`)
	var actual, expected any
	if err := json.Unmarshal(got, &actual); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("got %s; want %s", got, want)
	}
	if !json.Valid(got) || !containsJSONNumber(got, `9007199254740993`) {
		t.Fatalf("large number altered: %s", got)
	}
}

func containsJSONNumber(raw []byte, number string) bool {
	return bytes.Contains(raw, []byte(number))
}

func TestMergeConfigJSONRemovesOnlyChangedPanelFields(t *testing.T) {
	got, err := mergeConfigJSON([]byte(`{"tls":{"server_name":"old","custom":true},"unmodelled":1}`), []byte(`{"tls":{"server_name":"old"}}`), []byte(`{"tls":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"tls":{"custom":true},"unmodelled":1}` {
		t.Fatalf("unexpected result: %s", got)
	}
}

func TestMergeConfigSequenceReordersByIdentityAndPreservesUnknownRules(t *testing.T) {
	slots := []configMergeSlot{
		{Original: json.RawMessage(`{"action":"future-action","large":9007199254740993}`)},
		{ID: "10", Original: json.RawMessage(`{"action":"sniff","timeout":"2s"}`), Baseline: json.RawMessage(`{"action":"sniff"}`)},
		{Original: json.RawMessage(`{"action":"resolve","strategy":"prefer_ipv4"}`)},
		{ID: "11", Original: json.RawMessage(`{"port":[443],"outbound":"direct","override_port":8443}`), Baseline: json.RawMessage(`{"action":"route","port":[443],"outbound":"direct"}`)},
	}
	got, err := mergeConfigSequence(slots, []configMergeItem{
		{ID: "11", Projection: json.RawMessage(`{"action":"route","port":[80],"outbound":"direct"}`)},
		{ID: "10", Projection: json.RawMessage(`{"action":"sniff"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("entries = %d", len(got))
	}
	if !bytes.Contains(got[0], []byte(`9007199254740993`)) || !bytes.Contains(got[1], []byte(`"override_port":8443`)) || !bytes.Contains(got[1], []byte(`"port":[80]`)) || !bytes.Contains(got[3], []byte(`"timeout":"2s"`)) || !bytes.Contains(got[2], []byte(`"action":"resolve"`)) {
		t.Fatalf("incorrect identity merge: %s", got)
	}
	got, err = mergeConfigSequence(slots, []configMergeItem{{ID: "11", Projection: slots[3].Baseline}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !bytes.Contains(got[1], []byte(`"action":"resolve"`)) || !bytes.Contains(got[2], []byte(`"override_port":8443`)) {
		t.Fatalf("deletion lost or reassigned unknown fields: %s", got)
	}
}

func TestMergeConfigSequenceRejectsDuplicateIdentities(t *testing.T) {
	_, err := mergeConfigSequence(nil, []configMergeItem{{ID: "1", Projection: json.RawMessage(`{}`)}, {ID: "1", Projection: json.RawMessage(`{}`)}})
	if err == nil {
		t.Fatal("duplicate identities accepted")
	}
}

func TestMergeConfigJSONKeepsNestedObjectFieldsAcrossInsertionAndReorder(t *testing.T) {
	got, err := mergeConfigJSON([]byte(`{"users":[{"name":"a","password":"old-a","custom":1},{"name":"b","password":"old-b","custom":2}]}`), []byte(`{"users":[{"name":"a","password":"old-a"},{"name":"b","password":"old-b"}]}`), []byte(`{"users":[{"name":"b","password":"new-b"},{"name":"new","password":"new"},{"name":"a","password":"old-a"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"users":[{"custom":2,"name":"b","password":"new-b"},{"name":"new","password":"new"},{"custom":1,"name":"a","password":"old-a"}]}` {
		t.Fatalf("nested array merge lost identities: %s", got)
	}
}

func TestMergeConfigJSONEditsFirstImportedCredentialWithoutDroppingOtherUsers(t *testing.T) {
	got, err := mergeConfigJSON([]byte(`{"users":[{"name":"alice","uuid":"old","custom":true},{"name":"bob","uuid":"keep"}]}`), []byte(`{"users":[{"name":"user","uuid":"old"}]}`), []byte(`{"users":[{"name":"user","uuid":"new"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"users":[{"custom":true,"name":"alice","uuid":"new"},{"name":"bob","uuid":"keep"}]}` {
		t.Fatalf("manual users were duplicated or overwritten: %s", got)
	}
}
