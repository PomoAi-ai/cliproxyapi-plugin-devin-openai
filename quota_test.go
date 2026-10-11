package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

// A reversed percentage, wrong nesting, or treating omitted proto3 zero as unavailable fails here.
func TestSeatQuotaMapsWindowsAndProtoDefaults(t *testing.T) {
	body, err := hex.DecodeString("0a156a130a05120350726f704b78198801901c9001a038")
	if err != nil {
		t.Fatal(err)
	}
	quota, err := parseSeatQuota(body)
	if err != nil {
		t.Fatal(err)
	}
	if quota.Subscription == nil || quota.Subscription.Plan != "Pro" {
		t.Fatal(quota)
	}
	buckets := quota.Groups[0].Buckets
	if buckets[0].Window != "daily" || buckets[0].RemainingFraction != .75 || buckets[0].ResetTime != "1970-01-01T01:00:00Z" || buckets[1].Window != "weekly" || buckets[1].RemainingFraction != .25 || buckets[1].ResetTime != "1970-01-01T02:00:00Z" {
		t.Fatal(buckets)
	}
	for _, fixture := range []string{"0a026a00", "0a066a0470007800", "0a066a0470647864"} {
		body, _ := hex.DecodeString(fixture)
		quota, err := parseSeatQuota(body)
		if err != nil {
			t.Fatal(fixture, err)
		}
		expected := float64(0)
		if fixture == "0a066a0470647864" {
			expected = 1
		}
		for _, bucket := range quota.Groups[0].Buckets {
			if bucket.RemainingFraction != expected || bucket.ResetTime != "" {
				t.Fatal(fixture, bucket)
			}
		}
	}
}

// Invalid wire data or missing plan structure must not be presented as a depleted account.
func TestSeatQuotaRejectsMalformedAndMissingStructure(t *testing.T) {
	for _, fixture := range []string{
		"", "0801", "0a00", "0a026801", "0a036a0270", "0a036a0170",
		"0a046a027065", "0a046a027865", "0a046a027200", "0a046a02ff01",
		"0a066a040a021000", "0a076a050a031201ff",
		"0a0d6a0b8801ffffffffffffffff7f",
	} {
		body, err := hex.DecodeString(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseSeatQuota(body); err == nil {
			t.Fatalf("accepted malformed quota %s", fixture)
		}
	}
	// Unknown fields are allowed by protobuf forward compatibility.
	quota, err := parseSeatQuota([]byte{0x10, 0x01, 0x0a, 0x04, 0x6a, 0x02, 0x20, 0x01})
	if err != nil || len(quota.Groups) != 1 {
		t.Fatal(quota, err)
	}
}

// This exercises the snake_case CPA ABI and the real outbound auth/protobuf contract.
func TestQuotaFetchUsesSelectedCredentialAndSeatOrigin(t *testing.T) {
	key := "devin-session-token$dummy"
	p := newPlugin(func(method string, payload []byte) ([]byte, error) {
		var req httpRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			t.Fatal(err)
		}
		if method != "host.http.do" || req.Callback != "quota-context" || req.Method != "POST" || req.URL != "http://127.0.0.1:8123"+seatStatusPath || req.Headers["Authorization"][0] != "Basic "+key+"-"+key || req.Headers["Content-Type"][0] != "application/proto" {
			t.Fatal("incorrect quota HTTP routing or authentication")
		}
		number, kind, consumed := protowire.ConsumeTag(req.Body)
		if number != 1 || kind != protowire.BytesType || consumed < 0 {
			t.Fatal("missing request metadata")
		}
		metadata, count := protowire.ConsumeBytes(req.Body[consumed:])
		if count != len(req.Body)-consumed {
			t.Fatal("unexpected protobuf request framing")
		}
		fields := make(map[protowire.Number]string)
		for len(metadata) > 0 {
			number, kind, n := protowire.ConsumeTag(metadata)
			if n < 0 || kind != protowire.BytesType {
				t.Fatal("malformed request metadata")
			}
			value, m := protowire.ConsumeString(metadata[n:])
			if m < 0 {
				t.Fatal("malformed request metadata value")
			}
			fields[number] = value
			metadata = metadata[n+m:]
		}
		if fields[3] != key || fields[1] != "chisel" || len(fields[31]) != 732 {
			t.Fatal("incorrect Devin seat client metadata")
		}
		if _, err := hex.DecodeString(fields[31]); err != nil {
			t.Fatal("invalid device fingerprint")
		}
		return encoded(httpResponse{200, []byte{0x0a, 0x04, 0x6a, 0x02, 0x70, 0x32}}), nil
	})
	raw := encoded(map[string]any{"provider": provider, "storage_json": encoded(authFile{Type: provider, APIKey: key, InferenceURL: "http://127.0.0.1:8123"}), "host_callback_id": "quota-context"})
	result, err := p.quota("quota.fetch", raw)
	if err != nil {
		t.Fatal(err)
	}
	quota := result.(quotaResponse)
	if quota.Groups[0].Buckets[0].RemainingFraction != .5 || quota.Groups[0].Buckets[1].RemainingFraction != 0 {
		t.Fatal(quota)
	}
	serialized := encoded(quota)
	if bytes.Contains(serialized, []byte(key)) || !bytes.Contains(serialized, []byte(`"remainingFraction":0`)) {
		t.Fatal("quota leaked credentials or lost depleted window")
	}
}

func TestQuotaRejectsServiceKeysAndRedactsUpstreamErrors(t *testing.T) {
	raw := func(key string) []byte {
		return encoded(map[string]any{"provider": provider, "storage_json": encoded(authFile{Type: provider, APIKey: key}), "host_callback_id": "context"})
	}
	p := newPlugin(func(string, []byte) ([]byte, error) {
		t.Fatal("unsupported credential made an HTTP request")
		return nil, nil
	})
	if _, err := p.quota("quota.fetch", raw("cog_secret")); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatal(err)
	}
	if _, err := p.quota("quota.reset", nil); err == nil {
		t.Fatal("quota reset accepted")
	}
	for _, status := range []int{401, 403, 429, 503} {
		p := newPlugin(func(string, []byte) ([]byte, error) {
			return encoded(httpResponse{status, []byte("devin-session-token$secret")}), nil
		})
		_, err := p.quota("quota.fetch", raw("devin-session-token$dummy"))
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal(status, err)
		}
	}
	p = newPlugin(func(string, []byte) ([]byte, error) { return nil, errors.New("secret") })
	if _, err := p.quota("quota.fetch", raw("devin-session-token$dummy")); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}
