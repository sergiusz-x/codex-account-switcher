package switcher

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestUsageWindowsUseReturnedDurationsAndClampPercent(t *testing.T) {
	duration := int64(300)
	reset := int64(1_800_000_000)
	windows := usageWindows(rateLimitSnapshot{
		Primary:   &rateLimitWin{UsedPercent: 125, WindowDurationMin: &duration, ResetsAt: &reset},
		Secondary: &rateLimitWin{UsedPercent: -5},
	})
	if len(windows) != 2 {
		t.Fatalf("got %d windows, want 2", len(windows))
	}
	if windows[0].Duration != 5*time.Hour || windows[0].Used != 100 || !windows[0].HasResetAt {
		t.Fatalf("unexpected primary window: %+v", windows[0])
	}
	if windows[1].Duration != 0 || windows[1].Used != 0 || windows[1].HasResetAt {
		t.Fatalf("unexpected secondary window: %+v", windows[1])
	}
}

func TestUsageResetsKeepOnlyAvailableEntries(t *testing.T) {
	title := "Daily reset\n"
	expires := int64(1_800_000_000)
	resets := usageResets(&resetCreditsSummary{
		AvailableCount: 2,
		Credits: []resetCreditRow{
			{Status: "available", Title: &title, ExpiresAt: &expires},
			{Status: "used", Title: &title},
			{Status: "available"},
		},
	})
	if len(resets) != 2 {
		t.Fatalf("got %d resets, want 2", len(resets))
	}
	if strings.ContainsAny(resets[0].Title, "\r\n") || !resets[0].HasExpiry {
		t.Fatalf("reset fields were not sanitized: %+v", resets[0])
	}
	if resets[1].HasExpiry {
		t.Fatalf("missing expiration should remain unknown: %+v", resets[1])
	}
}

func TestAuthDisplayMetadataReadsOnlySyntheticClaims(t *testing.T) {
	claims, err := json.Marshal(map[string]any{
		"email":                       "person@example.invalid",
		"https://api.openai.com/auth": map[string]string{"chatgpt_plan_type": "chatgpt_pro"},
	})
	if err != nil {
		t.Fatal(err)
	}
	segment := base64.RawURLEncoding.EncodeToString(claims)
	raw, err := json.Marshal(map[string]any{
		"tokens": map[string]string{"id_token": "header." + segment + ".signature"},
	})
	if err != nil {
		t.Fatal(err)
	}
	email, plan := authDisplayMetadata(raw)
	if email != "person@example.invalid" || plan != "chatgpt pro" {
		t.Fatalf("got email %q and plan %q", email, plan)
	}
}

func TestPrepareUsageAuthKeepsCodexFieldsAndDropsRefreshToken(t *testing.T) {
	lastRefresh := time.Now().UTC().Format(time.RFC3339Nano)
	accessToken := syntheticJWT(t, map[string]any{"exp": time.Now().Add(30 * time.Minute).Unix()})
	idToken := syntheticJWT(t, map[string]any{"email": "person@example.invalid"})
	raw, err := json.Marshal(map[string]any{
		"auth_mode":    "chatgpt",
		"last_refresh": lastRefresh,
		"tokens": map[string]string{
			"id_token":      idToken,
			"access_token":  accessToken,
			"refresh_token": "synthetic-refresh-token",
			"account_id":    "synthetic-account",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	prepared, err := prepareUsageAuth(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		LastRefresh string `json:"last_refresh"`
		Tokens      struct {
			IDToken      string `json:"id_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(prepared, &got); err != nil {
		t.Fatal(err)
	}
	if got.LastRefresh != lastRefresh || got.Tokens.IDToken != idToken || got.Tokens.RefreshToken != "" {
		t.Fatalf("prepared auth has unexpected required or refresh fields")
	}
}

func TestPrepareUsageAuthRejectsNearExpiryToken(t *testing.T) {
	accessToken := syntheticJWT(t, map[string]any{"exp": time.Now().Add(5 * time.Minute).Unix()})
	idToken := syntheticJWT(t, map[string]any{})
	raw, err := json.Marshal(map[string]any{
		"auth_mode":    "chatgpt",
		"last_refresh": time.Now().UTC().Format(time.RFC3339),
		"tokens": map[string]string{
			"id_token": idToken, "access_token": accessToken, "refresh_token": "synthetic", "account_id": "account",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareUsageAuth(raw); err == nil {
		t.Fatal("near-expiry token should be rejected to avoid refresh")
	}
}

func syntheticJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func TestJSONLineClientDoesNotExposeProtocolError(t *testing.T) {
	var sent strings.Builder
	client := jsonLineClient{
		stdin:   &sent,
		scanner: bufio.NewScanner(strings.NewReader(`{"id":7,"error":{"message":"secret response"}}` + "\n")),
	}
	var result map[string]any
	err := client.call(t.Context(), 7, "account/rateLimits/read", map[string]bool{}, &result)
	if err == nil || strings.Contains(err.Error(), "secret response") {
		t.Fatalf("unexpected protocol error: %v", err)
	}
	if !strings.Contains(sent.String(), `"method":"account/rateLimits/read"`) {
		t.Fatalf("request was not sent: %s", sent.String())
	}
}

func TestReadUsageReportsPartialFailuresAndHonorsCancellation(t *testing.T) {
	for _, cancelAfterFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%v", cancelAfterFirst), func(t *testing.T) {
			store := NewStore(t.TempDir())
			for i := range 2 {
				writeTestAuth(t, store.authPath(), fakeAuth(fmt.Sprintf("account-%d", i), fmt.Sprintf("subject-%d", i), "synthetic-invalid-expiry", ""))
				if _, err := store.SaveCurrent(fmt.Sprintf("Account %d", i), false); err != nil {
					t.Fatal(err)
				}
			}
			before := readTestFile(t, store.authPath())
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var rows []AccountUsage
			store.ReadUsage(ctx, func(row AccountUsage) {
				rows = append(rows, row)
				if cancelAfterFirst {
					cancel()
				}
			})
			want := 2
			if cancelAfterFirst {
				want = 1
			}
			if len(rows) != want {
				t.Fatalf("reported %d accounts, want %d", len(rows), want)
			}
			for _, row := range rows {
				if row.Error == "" || !row.UpdatedAt.IsZero() {
					t.Fatal("invalid saved token was reported as a successful read")
				}
			}
			if string(readTestFile(t, store.authPath())) != string(before) {
				t.Fatal("usage changed the current authentication file")
			}
		})
	}
}
