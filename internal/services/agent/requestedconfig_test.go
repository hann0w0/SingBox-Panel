package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hann0w0/singbox-panel/internal/domain/protocol"
)

func TestRequestedConfigSourceReturnsExactRequestOnlyForInstalledDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json.source")
	proxy := restrictedProxy("exit.example")
	proxy["tls"] = map[string]any{"enabled": true}
	raw := append([]byte(" \n"), restrictedConfig(t, proxy)...)
	raw = append(raw, '\n')
	var installed []byte
	command := protocol.ApplyConfigCmd{Config: raw, RestrictedOutboundTags: []string{"private"}}
	out, err := applyRequestedConfig(context.Background(), command, func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}, func(_ context.Context, prepared []byte) (string, error) {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("source was saved before apply succeeded")
		}
		var err error
		installed, err = prepareConfigBytes(prepared)
		return "applied", err
	}, path)
	if err != nil || out != "applied" {
		t.Fatalf("apply output=%q error=%v", out, err)
	}
	if bytes.Contains(installed, []byte(`"server": "exit.example"`)) || !bytes.Contains(installed, []byte(`"server": "1.1.1.1"`)) {
		t.Fatalf("actual configuration did not retain the pinned IP: %s", installed)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("source containing credentials has insecure permissions: %v, %v", info, err)
	}
	sourceBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var source requestedConfigSource
	if err := json.Unmarshal(sourceBytes, &source); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(installed)
	if source.InstalledSHA256 != hex.EncodeToString(digest[:]) || !bytes.Equal(source.Requested, raw) {
		t.Fatal("source snapshot did not retain the original request and installed-byte hash")
	}
	if got := readRequestedConfigAt(installed, path); !bytes.Equal(got, raw) {
		t.Fatalf("get_config source did not preserve original DNS and whitespace: %s", got)
	}
	for _, actual := range [][]byte{append(bytes.Clone(installed), '\n'), []byte(`{"manually":"edited"}`), nil} {
		if got := readRequestedConfigAt(actual, path); !bytes.Equal(got, actual) {
			t.Fatal("source masked a local edit or mismatched installed configuration")
		}
	}
	if !bytes.Contains(installed, []byte(`"server": "1.1.1.1"`)) {
		t.Fatal("reading source modified installed bytes")
	}
}

func TestRequestedConfigSourceIsRemovedAfterUnrestrictedApply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json.source")
	if err := os.WriteFile(path, []byte("old source"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := []byte(" {\"outbounds\":[{\"type\":\"direct\"}]} \n")
	_, err := applyRequestedConfig(context.Background(), protocol.ApplyConfigCmd{Config: raw}, func(context.Context, string) ([]netip.Addr, error) {
		t.Fatal("unrestricted apply performed DNS validation")
		return nil, nil
	}, func(_ context.Context, config []byte) (string, error) {
		if !bytes.Equal(config, raw) {
			t.Fatal("unrestricted raw apply changed its input")
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal("source was cleared before apply succeeded")
		}
		return "applied", nil
	}, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unrelated raw apply left an old source snapshot: %v", err)
	}
}

func TestRequestedConfigFailedApplyKeepsPreviousSource(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unrestricted", true: "restricted"}[restricted], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json.source")
			previous := []byte("previous source")
			if err := os.WriteFile(path, previous, 0o600); err != nil {
				t.Fatal(err)
			}
			command := protocol.ApplyConfigCmd{Config: restrictedConfig(t, restrictedProxy("1.1.1.1"))}
			if restricted {
				command.RestrictedOutboundTags = []string{"private"}
			}
			failure := errors.New("apply failed and rolled back")
			_, err := applyRequestedConfig(context.Background(), command, nil, func(context.Context, []byte) (string, error) {
				return "check failure", failure
			}, path)
			if !errors.Is(err, failure) {
				t.Fatalf("apply error swallowed: %v", err)
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, previous) {
				t.Fatalf("failed apply changed previous source: %s %v", got, err)
			}
		})
	}
}

func TestRequestedConfigSecurityFailureNeverCallsApply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json.source")
	_, err := applyRequestedConfig(context.Background(), protocol.ApplyConfigCmd{
		Config: restrictedConfig(t, restrictedProxy("exit.example")), RestrictedOutboundTags: []string{"private"},
	}, func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("127.0.0.1")}, nil
	}, func(context.Context, []byte) (string, error) {
		t.Fatal("unsafe DNS was passed to ApplyConfig")
		return "", nil
	}, path)
	if err == nil || !strings.Contains(err.Error(), "unsafe address") {
		t.Fatalf("security failure missing: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("security failure persisted a source snapshot")
	}
}

func TestRequestedConfigSourceErrorsReportAlreadyAppliedState(t *testing.T) {
	command := protocol.ApplyConfigCmd{Config: restrictedConfig(t, restrictedProxy("1.1.1.1")), RestrictedOutboundTags: []string{"private"}}
	for _, path := range []string{filepath.Join(t.TempDir(), "missing", "source"), t.TempDir()} {
		applied := false
		out, err := applyRequestedConfig(context.Background(), command, nil, func(context.Context, []byte) (string, error) {
			applied = true
			return "config applied", nil
		}, path)
		if !applied || out != "config applied" || err == nil || !strings.Contains(err.Error(), "config applied, but saving requested config source failed") {
			t.Fatalf("source persistence failure hidden: %q %v", out, err)
		}
	}
}

func TestRequestedConfigMissingCorruptOrOversizedSourceFallsBackToActual(t *testing.T) {
	actual := []byte(`{"outbounds":[{"tag":"private","server":"1.1.1.1"}]}`)
	path := filepath.Join(t.TempDir(), "config.json.source")
	if got := readRequestedConfigAt(actual, path); !bytes.Equal(got, actual) {
		t.Fatal("missing sidecar changed actual config")
	}
	digest := sha256.Sum256(actual)
	for _, contents := range [][]byte{
		[]byte(`{`),
		[]byte(`null`),
		[]byte(`{"installed_sha256":"wrong","requested_config":"e30="}`),
		mustRequestedSource(t, requestedConfigSource{InstalledSHA256: hex.EncodeToString(digest[:]), Requested: []byte("invalid JSON")}),
		bytes.Repeat([]byte("x"), 2*maxConfigSize+1025),
	} {
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := readRequestedConfigAt(actual, path); !bytes.Equal(got, actual) {
			t.Fatal("corrupt or oversized sidecar hid installed configuration")
		}
	}
}

func mustRequestedSource(t *testing.T, source requestedConfigSource) []byte {
	t.Helper()
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRestrictedOutboundProtocolFieldsAndRegistrationCapability(t *testing.T) {
	command := protocol.ApplyConfigCmd{Config: json.RawMessage(`{}`), RestrictedOutboundTags: []string{"private"}}
	raw, err := json.Marshal(command)
	if err != nil || !bytes.Contains(raw, []byte(`"restricted_outbound_tags":["private"]`)) {
		t.Fatalf("restricted tags wire field missing: %s %v", raw, err)
	}
	var decoded protocol.ApplyConfigCmd
	if err := json.Unmarshal(raw, &decoded); err != nil || !reflect.DeepEqual(decoded.RestrictedOutboundTags, command.RestrictedOutboundTags) {
		t.Fatalf("restricted tags did not decode: %+v %v", decoded, err)
	}
	previousEndpoints := publicIPv4Endpoints
	publicIPv4Endpoints = nil
	defer func() { publicIPv4Endpoints = previousEndpoints }()
	registration := (&Agent{}).register()
	if !registration.RestrictedOutboundValidation {
		t.Fatal("agent registration does not advertise restricted validation")
	}
	raw, err = json.Marshal(registration)
	if err != nil || !bytes.Contains(raw, []byte(`"restricted_outbound_validation":true`)) {
		t.Fatalf("validation capability wire field missing: %s %v", raw, err)
	}
	for _, payload := range []any{protocol.ApplyConfigCmd{Config: json.RawMessage(`{}`)}, protocol.RegisterEvt{}} {
		raw, err := json.Marshal(payload)
		if err != nil || bytes.Contains(raw, []byte(`restricted_outbound`)) {
			t.Fatalf("optional fields not omitted: %s %v", raw, err)
		}
	}
}
