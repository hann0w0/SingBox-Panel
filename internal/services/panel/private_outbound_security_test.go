package panel

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/model"
)

func TestUserOutboundImportRejectsUnsafeTargets(t *testing.T) {
	fixture := newForwardFixture(t)
	fixture.router.POST("/import", fixture.app.importUserOutbound)
	for _, host := range []string{"127.0.0.1", "0.0.0.0", "localhost", "10.0.0.1", "169.254.169.254", "100.100.100.200", "[::1]", "[fd00:ec2::254]"} {
		t.Run(host, func(t *testing.T) {
			body := fmt.Sprintf(`{"uri":"vless://11111111-2222-4333-8444-555555555555@%s:9090#unsafe"}`, host)
			response := serveJSONRequest(t, fixture.router, http.MethodPost, "/import", body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("unsafe outbound accepted: %d %s", response.Code, response.Body)
			}
		})
	}
	var count int64
	fixture.db.Model(&model.Outbound{}).Count(&count)
	if count != 1 {
		t.Fatalf("unsafe imports persisted %d rows", count-1)
	}
}

func TestUserOutboundConcurrentImportsAllocateDistinctTags(t *testing.T) {
	fixture := newForwardFixture(t)
	fixture.router.POST("/import", fixture.app.importUserOutbound)
	responses := make(chan int, 2)
	for range 2 {
		go func() {
			response := serveJSONRequest(t, fixture.router, http.MethodPost, "/import", `{"uri":"vless://11111111-2222-4333-8444-555555555555@exit.example:443#same"}`)
			responses <- response.Code
		}()
	}
	for range 2 {
		if status := <-responses; status != http.StatusOK {
			t.Fatalf("import status=%d", status)
		}
	}
	var rows []model.Outbound
	fixture.db.Where("tag LIKE ?", "same%").Order("tag").Find(&rows)
	if len(rows) != 2 || rows[0].Tag == rows[1].Tag {
		t.Fatalf("tags collide: %+v", rows)
	}
}
