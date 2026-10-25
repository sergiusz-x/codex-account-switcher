package switcher

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"codex-account-switcher/internal/version"
)

const (
	usageTimeout          = 15 * time.Second
	minimumUsageTokenLife = 10 * time.Minute
)

type AccountUsage struct {
	ProfileID         string
	Name              string
	Email             string
	Plan              string
	PlanFromStatus    bool
	Windows           []UsageWindow
	Resets            []UsageReset
	ResetCount        int
	ResetCountKnown   bool
	ResetDetailsKnown bool
	UpdatedAt         time.Time
	Error             string
}

type UsageWindow struct {
	Duration   time.Duration
	Used       int
	ResetsAt   time.Time
	HasResetAt bool
}

type UsageReset struct {
	Title     string
	ResetType string
	ExpiresAt time.Time
	HasExpiry bool
}

type usageProfile struct {
	profile Profile
	raw     []byte
	email   string
	plan    string
	err     string
}

type usageAuth struct {
	Mode        string          `json:"auth_mode"`
	LastRefresh json.RawMessage `json:"last_refresh"`
	Tokens      struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

type usageResponse struct {
	RateLimits          rateLimitSnapshot            `json:"rateLimits"`
	RateLimitsByLimitID map[string]rateLimitSnapshot `json:"rateLimitsByLimitId"`
	ResetCredits        *resetCreditsSummary         `json:"rateLimitResetCredits"`
}

type rateLimitSnapshot struct {
	PlanType  *string       `json:"planType"`
	Primary   *rateLimitWin `json:"primary"`
	Secondary *rateLimitWin `json:"secondary"`
}

type rateLimitWin struct {
	UsedPercent       int    `json:"usedPercent"`
	WindowDurationMin *int64 `json:"windowDurationMins"`
	ResetsAt          *int64 `json:"resetsAt"`
}

type resetCreditsSummary struct {
	AvailableCount int              `json:"availableCount"`
	Credits        []resetCreditRow `json:"credits"`
}

type resetCreditRow struct {
	ResetType string  `json:"resetType"`
	Status    string  `json:"status"`
	Title     *string `json:"title"`
	ExpiresAt *int64  `json:"expiresAt"`
}

// ReadUsage reads every saved account through Codex's documented local
// App Server protocol, reporting each account as it finishes. Each reader has
// an isolated home and no refresh token. The callback runs serially.
func (s *Store) ReadUsage(ctx context.Context, report func(AccountUsage)) {
	if ctx.Err() != nil {
		return
	}
	profiles, err := s.usageProfiles()
	if err != nil {
		rows, _ := s.List()
		for _, profile := range rows {
			if ctx.Err() != nil {
				return
			}
			report(AccountUsage{ProfileID: profile.ID, Name: profile.Name, Error: err.Error()})
		}
		return
	}

	for _, saved := range profiles {
		if err := ctx.Err(); err != nil {
			break
		}
		row := AccountUsage{
			ProfileID: saved.profile.ID,
			Name:      saved.profile.Name,
			Email:     saved.email,
			Plan:      saved.plan,
			Error:     saved.err,
		}
		if row.Error == "" {
			usage, err := readProfileUsage(ctx, saved.raw)
			if err != nil {
				row.Error = err.Error()
			} else {
				snapshot := usage.RateLimits
				if codex, ok := usage.RateLimitsByLimitID["codex"]; ok {
					snapshot = codex
				}
				if snapshot.PlanType != nil && *snapshot.PlanType != "" {
					row.Plan = planLabel(*snapshot.PlanType)
					row.PlanFromStatus = row.Plan != ""
				}
				row.Windows = usageWindows(snapshot)
				row.Resets = usageResets(usage.ResetCredits)
				if usage.ResetCredits != nil {
					row.ResetCount = max(0, usage.ResetCredits.AvailableCount)
					row.ResetCountKnown = true
					row.ResetDetailsKnown = usage.ResetCredits.Credits != nil
				}
				row.UpdatedAt = time.Now()
			}
		}
		if ctx.Err() != nil {
			return
		}
		report(row)
	}
}

func (s *Store) usageProfiles() (profiles []usageProfile, retErr error) {
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := unlock(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("release switcher lock: %w", err))
		}
	}()

	data, err := s.loadState()
	if err != nil {
		return nil, err
	}
	var currentRaw []byte
	var currentIdentity identity
	if raw, id, err := s.readCurrent(); err == nil {
		currentRaw, currentIdentity = raw, id
	} else if !errors.Is(err, ErrNoAuth) && !errors.Is(err, ErrInvalidAuth) {
		return nil, fmt.Errorf("read current login for account status: %w", err)
	}

	profiles = make([]usageProfile, 0, len(data.Profiles))
	for _, profile := range data.Profiles {
		saved := usageProfile{profile: profile}
		raw, id, err := readAuth(s.profilePath(profile.ID))
		if err != nil {
			saved.err = "Saved login is unavailable; use More > Renew login."
			profiles = append(profiles, saved)
			continue
		}
		if id.key() != profile.Identity {
			saved.err = "Saved login does not match this profile."
			profiles = append(profiles, saved)
			continue
		}
		if id.key() == currentIdentity.key() && len(currentRaw) > 0 {
			raw = currentRaw
		}
		saved.raw = raw
		saved.email, saved.plan = authDisplayMetadata(raw)
		profiles = append(profiles, saved)
	}
	return profiles, nil
}

func readProfileUsage(parent context.Context, raw []byte) (usage usageResponse, retErr error) {
	return readProfileUsageWithCodex(parent, raw, "", nil)
}

func readProfileUsageWithCodex(parent context.Context, raw []byte, binary string, config []byte) (usage usageResponse, retErr error) {
	credentials, err := prepareUsageAuth(raw)
	if err != nil {
		return usage, err
	}

	home, err := os.MkdirTemp("", "codex-switch-usage-")
	if err != nil {
		return usage, errors.New("cannot create a private temporary status home")
	}
	defer func() {
		if err := os.RemoveAll(home); err != nil {
			retErr = errors.Join(retErr, errors.New("could not remove private temporary status credentials"))
		}
	}()
	if err := secureDataDir(home); err != nil {
		return usage, errors.New("cannot protect the temporary status home")
	}
	if err := atomicWrite(filepath.Join(home, "auth.json"), credentials, 0o600); err != nil {
		return usage, errors.New("cannot prepare temporary Codex credentials")
	}
	if config == nil {
		config = []byte("cli_auth_credentials_store = \"file\"\ncheck_for_update_on_startup = false\n[analytics]\nenabled = false\n")
	}
	if err := atomicWrite(filepath.Join(home, "config.toml"), config, 0o600); err != nil {
		return usage, errors.New("cannot configure the temporary status home")
	}

	ctx, cancel := context.WithTimeout(parent, usageTimeout)
	defer cancel()
	var cmd *exec.Cmd
	if binary == "" {
		cmd, err = newCodexCommand(ctx, "app-server", "--listen", "stdio://")
	} else {
		cmd, err = newCodexCommandAt(ctx, binary, "app-server", "--listen", "stdio://")
	}
	if err != nil {
		return usage, errors.New("Codex CLI was not found or could not be started")
	}
	cmd.Dir = home
	cmd.WaitDelay = 500 * time.Millisecond
	cmd.Env = usageEnvironment(home)
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return usage, errors.New("cannot start the isolated Codex status reader")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return usage, errors.New("cannot read isolated Codex status")
	}
	if err := cmd.Start(); err != nil {
		return usage, errors.New("cannot start isolated Codex status reader; check that Codex CLI is installed")
	}
	defer func() {
		if err := stdin.Close(); err != nil {
			retErr = errors.Join(retErr, errors.New("could not close the isolated Codex status reader input"))
		}
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			retErr = errors.Join(retErr, errors.New("could not stop the isolated Codex status reader"))
		}
		if err := cmd.Wait(); err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				retErr = errors.Join(retErr, errors.New("could not clean up the isolated Codex status reader"))
			}
		}
	}()

	scanner := bufio.NewScanner(io.LimitReader(stdout, 8*maxAuthSize))
	scanner.Buffer(make([]byte, 4096), maxAuthSize)
	client := jsonLineClient{stdin: stdin, scanner: scanner}
	var initialized struct {
		CodexHome string `json:"codexHome"`
	}
	if err := client.call(ctx, 1, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "codex_account_switcher", "version": version.Version()},
		"capabilities": map[string]any{},
	}, &initialized); err != nil {
		return usage, err
	}
	wantHome, wantErr := os.Stat(home)
	gotHome, gotErr := os.Stat(initialized.CodexHome)
	if wantErr != nil || gotErr != nil || !os.SameFile(wantHome, gotHome) {
		return usage, errors.New("Codex status reader opened an unexpected home")
	}
	if err := client.notify("initialized", map[string]any{}); err != nil {
		return usage, err
	}
	if err := client.call(ctx, 2, "account/rateLimits/read", map[string]bool{"supportsLunaReserve": false}, &usage); err != nil {
		return usage, err
	}
	return usage, nil
}

func prepareUsageAuth(raw []byte) ([]byte, error) {
	var auth usageAuth
	if err := json.Unmarshal(raw, &auth); err != nil || auth.Mode != "chatgpt" || auth.Tokens.AccessToken == "" || auth.Tokens.AccountID == "" {
		return nil, errors.New("saved login is incomplete; use More > Renew login")
	}
	var lastRefresh time.Time
	if len(auth.LastRefresh) == 0 || json.Unmarshal(auth.LastRefresh, &lastRefresh) != nil || lastRefresh.IsZero() {
		return nil, errors.New("saved login is missing required Codex status metadata")
	}
	expiresAt, ok := accessTokenExpiry(auth.Tokens.AccessToken)
	if !ok || time.Until(expiresAt) <= minimumUsageTokenLife {
		return nil, errors.New("access token is expired, near expiry, or has no readable expiry; run Codex normally, then refresh Usage")
	}
	// Codex requires the ID token and last_refresh to load managed ChatGPT auth.
	// An empty refresh token prevents this isolated reader from rotating credentials.
	auth.Tokens.RefreshToken = ""
	credentials, err := json.Marshal(auth)
	if err != nil {
		return nil, errors.New("cannot prepare isolated account status")
	}
	return credentials, nil
}

func accessTokenExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		ExpiresAt int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.ExpiresAt <= 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.ExpiresAt, 0), true
}

type jsonLineClient struct {
	stdin   io.Writer
	scanner *bufio.Scanner
}

func (c jsonLineClient) notify(method string, params any) error {
	return c.send(map[string]any{"method": method, "params": params})
}

func (c jsonLineClient) call(ctx context.Context, id int, method string, params, target any) error {
	if err := ctx.Err(); err != nil {
		return errors.New("status request was canceled")
	}
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for c.scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return errors.New("status request timed out or was canceled")
		}
		line := c.scanner.Bytes()
		var response struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(line, &response) != nil {
			return errors.New("Codex returned an invalid status response")
		}
		if response.ID == nil || *response.ID != id {
			continue
		}
		if len(response.Error) > 0 && string(response.Error) != "null" {
			return errors.New("Codex could not read this account's status; its login may need renewal")
		}
		if json.Unmarshal(response.Result, target) != nil {
			return errors.New("Codex returned an unsupported status response")
		}
		return nil
	}
	if err := ctx.Err(); err != nil {
		return errors.New("status request timed out or was canceled")
	}
	return errors.New("Codex status reader closed or returned an oversized response")
}

func (c jsonLineClient) send(value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return errors.New("cannot encode a Codex status request")
	}
	if _, err := fmt.Fprintf(c.stdin, "%s\n", raw); err != nil {
		return errors.New("cannot send a Codex status request")
	}
	return nil
}

func usageEnvironment(home string) []string {
	return filterUsageEnvironment(os.Environ(), home)
}

func filterUsageEnvironment(environment []string, home string) []string {
	var env []string
	for _, item := range environment {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if isSensitiveUsageEnvironmentKey(key) {
			continue
		}
		env = append(env, item)
	}
	return setEnvironmentValue(env, "CODEX_HOME", home)
}

func isSensitiveUsageEnvironmentKey(key string) bool {
	for _, sensitive := range []string{
		"CODEX_HOME", "OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN",
		"CODEX_INTERNAL_ORIGINATOR_OVERRIDE", "CODEX_REFRESH_TOKEN_URL_OVERRIDE",
		"OPENAI_BASE_URL", "CODEX_BASE_URL", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY",
	} {
		if environmentKeyEqual(key, sensitive) {
			return true
		}
	}
	return false
}

func authDisplayMetadata(raw []byte) (email, plan string) {
	var auth usageAuth
	if json.Unmarshal(raw, &auth) != nil {
		return "", ""
	}
	parts := strings.Split(auth.Tokens.IDToken, ".")
	if len(parts) != 3 {
		return "", ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var claims struct {
		Email string `json:"email"`
		Auth  struct {
			Plan string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return "", ""
	}
	return safeUsageText(claims.Email, 120), planLabel(claims.Auth.Plan)
}

func usageWindows(snapshot rateLimitSnapshot) []UsageWindow {
	var windows []UsageWindow
	for _, source := range []*rateLimitWin{snapshot.Primary, snapshot.Secondary} {
		if source == nil {
			continue
		}
		window := UsageWindow{Used: max(0, min(100, source.UsedPercent))}
		if source.WindowDurationMin != nil && *source.WindowDurationMin > 0 && *source.WindowDurationMin <= 525600 {
			window.Duration = time.Duration(*source.WindowDurationMin) * time.Minute
		}
		if source.ResetsAt != nil && *source.ResetsAt > 0 {
			window.ResetsAt, window.HasResetAt = time.Unix(*source.ResetsAt, 0).Local(), true
		}
		windows = append(windows, window)
	}
	return windows
}

func usageResets(summary *resetCreditsSummary) []UsageReset {
	if summary == nil || summary.Credits == nil {
		return nil
	}
	resets := make([]UsageReset, 0, min(len(summary.Credits), 20))
	for _, credit := range summary.Credits {
		if credit.Status != "available" || len(resets) >= 20 {
			continue
		}
		reset := UsageReset{ResetType: safeUsageText(credit.ResetType, 80)}
		if credit.Title != nil {
			reset.Title = safeUsageText(*credit.Title, 120)
		}
		if credit.ExpiresAt != nil && *credit.ExpiresAt > 0 {
			reset.ExpiresAt, reset.HasExpiry = time.Unix(*credit.ExpiresAt, 0).Local(), true
		}
		resets = append(resets, reset)
	}
	return resets
}

func planLabel(plan string) string {
	if plan == "" || plan == "unknown" {
		return ""
	}
	if strings.HasPrefix(plan, "chatgpt_") {
		name := strings.TrimPrefix(plan, "chatgpt_")
		words := strings.FieldsFunc(name, func(r rune) bool { return r == '_' || r == ' ' })
		for i, word := range words {
			if word != "" {
				words[i] = strings.ToUpper(word[:1]) + word[1:]
			}
		}
		return safeUsageText("ChatGPT "+strings.Join(words, " "), 80)
	}
	return safeUsageText(strings.ReplaceAll(plan, "_", " "), 80)
}

func safeUsageText(value string, limit int) string {
	runes := []rune(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value))
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return strings.TrimSpace(string(runes))
}
