package authorizer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func newAuth() *Authorizer {
	return &Authorizer{
		Creds: Credentials{Username: "awtrix", Password: "hunter2"},
		Policy: PolicyConfig{
			Region:          "ap-southeast-2",
			AccountID:       "123456789012",
			AllowedClientID: "awtrix_103",
			AllowedTopic:    "awtrix_103/custom/overhead",
		},
	}
}

func makeReq(user, pass, clientID string) Request {
	return Request{
		ProtocolData: ProtocolData{
			MQTT: MQTTProtocolData{
				Username: user,
				Password: base64.StdEncoding.EncodeToString([]byte(pass)),
				ClientID: clientID,
			},
		},
	}
}

func TestHandle_HappyPath(t *testing.T) {
	a := newAuth()
	resp, err := a.Handle(context.Background(), makeReq("awtrix", "hunter2", "awtrix_103"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !resp.IsAuthenticated {
		t.Fatal("expected authenticated")
	}
	if len(resp.PolicyDocuments) != 1 {
		t.Fatalf("want 1 policy, got %d", len(resp.PolicyDocuments))
	}
	// AWS rejects principalIds with non-alphanumeric chars, so the
	// authorizer strips them: "awtrix_103" → "awtrix103".
	if resp.PrincipalID != "awtrix103" {
		t.Errorf("principal = %q", resp.PrincipalID)
	}

	// Spot-check the policy.
	var doc struct {
		Statement []struct {
			Action   any `json:"Action"`
			Resource any `json:"Resource"`
		}
	}
	if err := json.Unmarshal([]byte(resp.PolicyDocuments[0]), &doc); err != nil {
		t.Fatalf("policy not valid JSON: %v", err)
	}
	// Connect + Subscribe + Receive + Publish.
	if len(doc.Statement) != 4 {
		t.Errorf("want 4 statements, got %d", len(doc.Statement))
	}
	for _, s := range doc.Statement {
		res, _ := s.Resource.(string)
		if !strings.Contains(res, "ap-southeast-2:123456789012") {
			t.Errorf("resource missing region/account: %s", res)
		}
	}
}

func TestHandle_Rejections(t *testing.T) {
	tests := []struct {
		name string
		req  Request
	}{
		{"wrong_password", makeReq("awtrix", "nope", "awtrix_103")},
		{"wrong_username", makeReq("admin", "hunter2", "awtrix_103")},
		{"wrong_client_id", makeReq("awtrix", "hunter2", "rogue_client")},
		{"empty_credentials", makeReq("", "", "awtrix_103")},
		{
			name: "bad_base64_password",
			req: Request{ProtocolData: ProtocolData{MQTT: MQTTProtocolData{
				Username: "awtrix", Password: "not!base64", ClientID: "awtrix_103",
			}}},
		},
	}
	a := newAuth()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := a.Handle(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if resp.IsAuthenticated {
				t.Errorf("expected reject, got auth=true")
			}
			if len(resp.PolicyDocuments) != 0 {
				t.Errorf("denied response should have no policy")
			}
		})
	}
}

func TestBuildPolicy_Incomplete(t *testing.T) {
	if _, err := buildPolicy(PolicyConfig{Region: "x"}); err == nil {
		t.Error("expected error for incomplete config")
	}
}
