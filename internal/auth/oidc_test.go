package auth

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestUserFromTokenDecodesClaimsAndGroups(t *testing.T) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payloadBytes, _ := json.Marshal(map[string]any{
		"sub":                "user-1",
		"email":              "ada@example.com",
		"name":               "Ada Lovelace",
		"preferred_username": "ada",
		"client_id":          "client-1",
		"groups": []map[string]any{
			{"groupId": "cnips-dev", "groupType": "tenant", "roles": []string{"ADMIN"}},
		},
	})
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)

	user := UserFromToken(header + "." + payload + ".sig")

	if user == nil {
		t.Fatal("user is nil")
	}
	if user.Email != "ada@example.com" || user.Subject != "user-1" || user.ClientID != "client-1" {
		t.Fatalf("unexpected user: %#v", user)
	}
	if len(user.Groups) != 1 || user.Groups[0].GroupID != "cnips-dev" || user.Groups[0].Roles[0] != "ADMIN" {
		t.Fatalf("unexpected groups: %#v", user.Groups)
	}
	if user.Claims["preferred_username"] != "ada" {
		t.Fatalf("claims were not preserved: %#v", user.Claims)
	}
}

func TestNormalizeAPIURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"remote without suffix", "https://dev.cnips.eu", "https://dev.cnips.eu/mgmt-srv"},
		{"remote trailing slash", "https://dev.cnips.eu/", "https://dev.cnips.eu/mgmt-srv"},
		{"remote whitespace", "  https://dev.cnips.eu  ", "https://dev.cnips.eu/mgmt-srv"},
		{"remote already has suffix", "https://dev.cnips.eu/mgmt-srv", "https://dev.cnips.eu/mgmt-srv"},
		{"remote suffix trailing slash", "https://dev.cnips.eu/mgmt-srv/", "https://dev.cnips.eu/mgmt-srv"},
		{"localhost untouched", "http://localhost:8090", "http://localhost:8090"},
		{"loopback ip untouched", "http://127.0.0.1:8090", "http://127.0.0.1:8090"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeAPIURL(tc.in); got != tc.want {
				t.Fatalf("NormalizeAPIURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
