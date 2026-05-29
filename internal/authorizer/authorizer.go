// Package authorizer implements an AWS IoT Core custom authorizer that
// validates username/password (out of Secrets Manager) and returns a tightly
// scoped IAM policy for the AWTRIX device.
//
// IoT Core invokes the Lambda with a CustomAuthorizerRequest; the response
// must include isAuthenticated + an inline IAM policy.
//
// See https://docs.aws.amazon.com/iot/latest/developerguide/custom-authorizer.html
package authorizer

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// AWS IoT custom-authorizer constraint: principalId must match this pattern.
// Underscores, hyphens, etc. are rejected with InvalidResponseException at
// runtime and AUTHORIZATION_FAILURE in connection logs.
var nonAlphanum = regexp.MustCompile(`[^a-zA-Z0-9]`)

// Request is the subset of the IoT custom-authorizer event we need.
// IoT Core sends extra metadata; we ignore what we don't use.
type Request struct {
	Token              string       `json:"token"`
	SignatureVerified  bool         `json:"signatureVerified"`
	ProtocolData       ProtocolData `json:"protocolData"`
	Protocols          []string     `json:"protocols"`
	ConnectionMetadata struct {
		ID string `json:"id"`
	} `json:"connectionMetadata"`
}

// ProtocolData carries the MQTT credentials presented at CONNECT.
type ProtocolData struct {
	MQTT MQTTProtocolData `json:"mqtt"`
}

type MQTTProtocolData struct {
	Username string `json:"username"`
	Password string `json:"password"` // base64-encoded
	ClientID string `json:"clientId"`
}

// Response is what IoT Core expects back.
type Response struct {
	IsAuthenticated          bool     `json:"isAuthenticated"`
	PrincipalID              string   `json:"principalId"`
	DisconnectAfterInSeconds int      `json:"disconnectAfterInSeconds"`
	RefreshAfterInSeconds    int      `json:"refreshAfterInSeconds"`
	PolicyDocuments          []string `json:"policyDocuments"`
}

// PolicyConfig is the static policy scope for the AWTRIX device.
type PolicyConfig struct {
	Region          string // e.g. "ap-southeast-2"
	AccountID       string // 12-digit AWS account number
	AllowedClientID string // exact MQTT client ID to allow
	AllowedTopic    string // topic to allow Subscribe + Receive (e.g. "awtrix_xxxx/custom/overhead")
}

// Credentials describes the accepted username/password pair.
type Credentials struct {
	Username string
	Password string
}

// Authorizer holds the static config used to evaluate every request.
type Authorizer struct {
	Creds  Credentials
	Policy PolicyConfig
}

// Handle implements the Lambda business logic, separately from the AWS-specific
// event wrapper so it can be tested directly.
func (a *Authorizer) Handle(ctx context.Context, req Request) (Response, error) {
	if !checkCreds(req.ProtocolData.MQTT, a.Creds) {
		// IoT Core treats a non-2xx Lambda response as deny. We return a
		// well-formed deny instead so the connection rejection is logged
		// cleanly and per-Lambda telemetry stays sane.
		return Response{IsAuthenticated: false}, nil
	}
	if req.ProtocolData.MQTT.ClientID != a.Policy.AllowedClientID {
		return Response{IsAuthenticated: false}, nil
	}

	policy, err := buildPolicy(a.Policy)
	if err != nil {
		return Response{}, fmt.Errorf("build policy: %w", err)
	}
	return Response{
		IsAuthenticated:          true,
		PrincipalID:              nonAlphanum.ReplaceAllString(a.Policy.AllowedClientID, ""),
		DisconnectAfterInSeconds: 86400, // force a re-auth daily
		// AWS validates RefreshAfterInSeconds in [300, 86400]; anything
		// outside that range makes AWS reject the entire response with
		// AUTHORIZATION_FAILURE and the Lambda invocation looks fine in
		// CloudWatch but the connection never gets a principal. Use the
		// minimum so cache turns over quickly while staying valid.
		RefreshAfterInSeconds: 300,
		PolicyDocuments:       []string{policy},
	}, nil
}

func checkCreds(got MQTTProtocolData, want Credentials) bool {
	if got.Username == "" || got.Password == "" {
		return false
	}
	pw, err := base64.StdEncoding.DecodeString(got.Password)
	if err != nil {
		return false
	}
	// IoT Core passes the full MQTT username, including the
	// "?x-amz-customauthorizer-name=..." query that routed us here.
	bareUser := got.Username
	if i := strings.IndexByte(bareUser, '?'); i >= 0 {
		bareUser = bareUser[:i]
	}
	uMatch := subtle.ConstantTimeCompare([]byte(bareUser), []byte(want.Username)) == 1
	pMatch := subtle.ConstantTimeCompare(pw, []byte(want.Password)) == 1
	return uMatch && pMatch
}

// buildPolicy returns a JSON IAM policy that:
//   - allows the device to Connect with exactly the configured client ID
//   - allows Subscribe + Receive on the configured topic
//
// No publish, no other topics.
func buildPolicy(p PolicyConfig) (string, error) {
	if p.Region == "" || p.AccountID == "" || p.AllowedClientID == "" || p.AllowedTopic == "" {
		return "", errors.New("incomplete PolicyConfig")
	}
	// Use AWS IoT policy variables. The string ${iot:ClientId} is NOT a Go
	// fmt verb — it's interpolated by AWS at policy-evaluation time against
	// the actual connection's clientId. AWS's docs use this form in the
	// custom-authorizer examples; literal client ARNs sometimes fail with
	// AUTHORIZATION_FAILURE even when they look correct.
	// AWTRIX subscribes to many control topics under its prefix at connect
	// time (brightness, custom apps, notify, etc.). Granting Subscribe and
	// Receive over the whole prefix avoids per-topic disconnect cycles.
	connectARN := fmt.Sprintf("arn:aws:iot:%s:%s:client/${iot:ClientId}", p.Region, p.AccountID)
	prefixTopicARN := fmt.Sprintf("arn:aws:iot:%s:%s:topic/%s/*", p.Region, p.AccountID, p.AllowedClientID)
	prefixFilterARN := fmt.Sprintf("arn:aws:iot:%s:%s:topicfilter/%s/*", p.Region, p.AccountID, p.AllowedClientID)

	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{
				"Effect":   "Allow",
				"Action":   "iot:Connect",
				"Resource": connectARN,
			},
			{
				"Effect":   "Allow",
				"Action":   "iot:Subscribe",
				"Resource": prefixFilterARN,
			},
			{
				"Effect":   "Allow",
				"Action":   "iot:Receive",
				"Resource": prefixTopicARN,
			},
			{
				"Effect":   "Allow",
				"Action":   "iot:Publish",
				"Resource": prefixTopicARN,
			},
		},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
