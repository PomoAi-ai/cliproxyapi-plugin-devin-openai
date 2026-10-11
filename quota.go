package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
)

const seatStatusPath = "/exa.seat_management_pb.SeatManagementService/GetUserStatus"

type quotaRequest struct {
	Provider       string `json:"provider"`
	StorageJSON    []byte `json:"storage_json"`
	HostCallbackID string `json:"host_callback_id"`
}

type quotaSubscription struct {
	Plan string `json:"plan,omitempty"`
}

type quotaBucket struct {
	Window            string  `json:"window"`
	RemainingFraction float64 `json:"remainingFraction"`
	ResetTime         string  `json:"resetTime,omitempty"`
}

type quotaGroup struct {
	DisplayName string        `json:"displayName"`
	Buckets     []quotaBucket `json:"buckets"`
}

type quotaResponse struct {
	Subscription *quotaSubscription `json:"subscription,omitempty"`
	Groups       []quotaGroup       `json:"groups"`
}

func (p *plugin) quota(method string, raw []byte) (any, error) {
	switch method {
	case "quota.identifier":
		return map[string]string{"identifier": provider}, nil
	case "quota.describe":
		return map[string]any{"supported_providers": []string{provider}, "display_name": "Devin seat quota", "supports_reset": false}, nil
	case "quota.reset":
		return nil, errors.New("Devin seat quota reset is not supported")
	case "quota.fetch":
		var r quotaRequest
		if json.Unmarshal(raw, &r) != nil {
			return nil, errors.New("invalid Devin quota request JSON")
		}
		if r.Provider != provider {
			return nil, errors.New("quota provider must be devin-openai")
		}
		if r.HostCallbackID == "" {
			return nil, errors.New("quota host HTTP callback context is required")
		}
		auth, err := parseAuth(r.StorageJSON, "")
		if err != nil {
			return nil, err
		}
		key := auth.Attributes["api_key"]
		if !strings.HasPrefix(key, "devin-session-token$") || len(key) == len("devin-session-token$") {
			return nil, errors.New("Devin seat quota requires a session token; service keys are unsupported")
		}
		body, err := seatStatusRequest(key)
		if err != nil {
			return nil, err
		}
		req := httpRequest{
			Callback: r.HostCallbackID,
			Method:   "POST",
			URL:      strings.TrimSuffix(auth.Attributes["base_url"], "/api/v1") + seatStatusPath,
			Headers: map[string][]string{
				"Authorization":            {"Basic " + key + "-" + key},
				"Connect-Protocol-Version": {"1"},
				"Content-Type":             {"application/proto"},
				"Accept":                   {"*/*"},
				"User-Agent":               {""},
			},
			Body: body,
		}
		payload, err := json.Marshal(req)
		if err != nil {
			return nil, errors.New("cannot encode Devin seat quota request")
		}
		result, err := p.call("host.http.do", payload)
		if err != nil {
			return nil, errors.New("Devin seat quota HTTP request failed")
		}
		var response httpResponse
		if json.Unmarshal(result, &response) != nil {
			return nil, errors.New("invalid Devin seat quota HTTP response")
		}
		switch response.StatusCode {
		case 200:
			return parseSeatQuota(response.Body)
		case 401:
			return nil, errors.New("Devin seat quota HTTP 401: sign in again")
		case 403:
			return nil, errors.New("Devin seat quota HTTP 403: access denied")
		default:
			return nil, fmt.Errorf("Devin seat quota HTTP %d", response.StatusCode)
		}
	default:
		return nil, errors.New("unsupported quota method")
	}
}

func seatStatusRequest(token string) ([]byte, error) {
	var fingerprint [366]byte
	if _, err := rand.Read(fingerprint[:]); err != nil {
		return nil, errors.New("cannot generate Devin seat device fingerprint")
	}
	var metadata []byte
	for _, field := range []struct {
		number protowire.Number
		value  string
	}{{1, "chisel"}, {2, "3000.10.21"}, {3, token}, {4, "en"}, {5, runtime.GOOS}, {7, "3000.10.21"}, {12, "chisel"}, {31, hex.EncodeToString(fingerprint[:])}} {
		metadata = protowire.AppendTag(metadata, field.number, protowire.BytesType)
		metadata = protowire.AppendString(metadata, field.value)
	}
	return protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), metadata), nil
}

// Only the seat plan structure establishes quota availability; proto3 omitted numeric fields mean zero.
func parseSeatQuota(body []byte) (quotaResponse, error) {
	if len(body) == 0 || len(body) > 4<<20 {
		return quotaResponse{}, errors.New("invalid Devin seat quota response size")
	}
	status, err := seatMessage(body, 1, "user_status")
	if err != nil {
		return quotaResponse{}, err
	}
	plan, err := seatMessage(status, 13, "plan_status")
	if err != nil {
		return quotaResponse{}, err
	}
	var daily, weekly, dailyReset, weeklyReset uint64
	var planName string
	err = eachSeatField(plan, func(number protowire.Number, kind protowire.Type, data []byte) error {
		switch number {
		case 1:
			if kind != protowire.BytesType {
				return errors.New("invalid Devin seat plan_info wire type")
			}
			info, _ := protowire.ConsumeBytes(data)
			return eachSeatField(info, func(n protowire.Number, k protowire.Type, value []byte) error {
				if n != 2 {
					return nil
				}
				if k != protowire.BytesType {
					return errors.New("invalid Devin seat plan name wire type")
				}
				text, _ := protowire.ConsumeBytes(value)
				if !utf8.Valid(text) {
					return errors.New("invalid Devin seat plan name")
				}
				planName = string(text)
				return nil
			})
		case 14, 15, 17, 18:
			if kind != protowire.VarintType {
				return errors.New("invalid Devin seat quota numeric wire type")
			}
			value, _ := protowire.ConsumeVarint(data)
			if (number == 14 || number == 15) && value > 100 {
				return errors.New("Devin seat quota percentage outside 0..100")
			}
			if (number == 17 || number == 18) && value > 253402300799 {
				return errors.New("Devin seat quota reset time outside RFC3339 range")
			}
			switch number {
			case 14:
				daily = value
			case 15:
				weekly = value
			case 17:
				dailyReset = value
			case 18:
				weeklyReset = value
			}
		}
		return nil
	})
	if err != nil {
		return quotaResponse{}, err
	}
	buckets := []quotaBucket{{Window: "daily", RemainingFraction: float64(daily) / 100}, {Window: "weekly", RemainingFraction: float64(weekly) / 100}}
	for i, seconds := range []uint64{dailyReset, weeklyReset} {
		if seconds != 0 {
			buckets[i].ResetTime = time.Unix(int64(seconds), 0).UTC().Format(time.RFC3339)
		}
	}
	response := quotaResponse{Groups: []quotaGroup{{DisplayName: "Devin seat quota (not gateway balance)", Buckets: buckets}}}
	if planName != "" {
		response.Subscription = &quotaSubscription{Plan: planName}
	}
	return response, nil
}

func seatMessage(data []byte, wanted protowire.Number, name string) ([]byte, error) {
	var message []byte
	found := false
	err := eachSeatField(data, func(number protowire.Number, kind protowire.Type, value []byte) error {
		if number != wanted {
			return nil
		}
		if kind != protowire.BytesType {
			return fmt.Errorf("invalid Devin seat %s wire type", name)
		}
		part, _ := protowire.ConsumeBytes(value)
		message = append(message, part...)
		found = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("Devin seat quota response missing %s", name)
	}
	return message, nil
}

func eachSeatField(data []byte, visit func(protowire.Number, protowire.Type, []byte) error) error {
	for len(data) > 0 {
		number, kind, n := protowire.ConsumeTag(data)
		if n < 0 {
			return errors.New("malformed Devin seat protobuf tag")
		}
		data = data[n:]
		n = protowire.ConsumeFieldValue(number, kind, data)
		if n < 0 {
			return errors.New("malformed Devin seat protobuf field")
		}
		if err := visit(number, kind, data[:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}
