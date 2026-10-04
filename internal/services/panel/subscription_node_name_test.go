package panel

import (
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestManagedNodeNamesFollowServerName(t *testing.T) {
	db := testDB(t)
	hk := model.Server{Name: "🇭🇰 香港", AgentToken: "name-hk", Address: "hk.example.com"}
	jp := model.Server{Name: "日本", AgentToken: "name-jp", Address: "jp.example.com"}
	us := model.Server{Name: "美国", AgentToken: "name-us", Address: "us.example.com"}
	for _, srv := range []*model.Server{&hk, &jp, &us} {
		if err := db.Create(srv).Error; err != nil {
			t.Fatal(err)
		}
	}
	hkSS := model.Inbound{ServerID: hk.ID, Tag: "ss-in", Type: model.InboundShadowsocks, ListenPort: 20001, Enabled: true}
	jpVLESS := model.Inbound{ServerID: jp.ID, Tag: "vless-a1b2c3", Type: model.InboundVLESS, ListenPort: 20002, Enabled: true}
	jpTrojan := model.Inbound{ServerID: jp.ID, Tag: "trojan-cdn", Type: model.InboundTrojan, ListenPort: 20003, Enabled: true}
	usVLESS := model.Inbound{ServerID: us.ID, Tag: "vless-in", Type: model.InboundVLESS, ListenPort: 20004, Enabled: true}
	usTrojan := model.Inbound{ServerID: us.ID, Tag: "trojan-in", Type: model.InboundTrojan, ListenPort: 20005, Enabled: true}
	for _, ib := range []*model.Inbound{&hkSS, &jpVLESS, &jpTrojan, &usVLESS, &usTrojan} {
		if err := db.Create(ib).Error; err != nil {
			t.Fatal(err)
		}
	}
	// The user holds every inbound on 香港 and 日本 but only one of the two
	// inbounds on 美国, so 美国 contributes a single node to this subscription.
	user := model.User{
		Email: "name-user", Password: "unused", Role: model.RoleUser, Enabled: true,
		ServerIDs:  []uint{hk.ID, jp.ID, us.ID},
		InboundIDs: []uint{hkSS.ID, jpVLESS.ID, jpTrojan.ID, usTrojan.ID},
		SubToken:   "name-token",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	nodes := (&App{db: db}).gatherNodes(&user)
	var got []string
	for _, n := range nodes {
		got = append(got, n.name)
	}
	want := []string{"🇭🇰 香港", "日本 - VLESS", "日本 - trojan-cdn", "美国"}
	if len(got) != len(want) {
		t.Fatalf("node names = %q; want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("node names = %q; want %q", got, want)
		}
	}
}
