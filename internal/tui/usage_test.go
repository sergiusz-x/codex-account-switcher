package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"codex-account-switcher/internal/switcher"
)

func dashboardModel(accounts int) model {
	m := model{activeID: "account-0", activeExists: true}
	for i := range accounts {
		id := fmt.Sprintf("account-%d", i)
		m.profiles = append(m.profiles, switcher.Profile{ID: id, Name: fmt.Sprintf("Account %d", i)})
		m.usageResults = append(m.usageResults, switcher.AccountUsage{
			ProfileID: id, Email: fmt.Sprintf("person-%d@example.invalid", i), Plan: "plus", PlanFromStatus: true,
			Windows: []switcher.UsageWindow{
				{Duration: 5 * time.Hour, Used: 20, HasResetAt: true, ResetsAt: time.Date(2026, 10, 22, 14, 30, 0, 0, time.Local)},
				{Duration: 7 * 24 * time.Hour, Used: 60},
			},
			ResetCount: i + 1, ResetCountKnown: true, UpdatedAt: time.Now(),
		})
	}
	return m
}

func TestResetExpiryUsesEarliestKnownDate(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		count  int
		known  bool
		resets []switcher.UsageReset
		want   string
	}{
		{name: "unknown count"},
		{name: "zero resets", known: true},
		{name: "missing expiry", count: 1, known: true, want: "Expiry unknown"},
		{name: "zero date", count: 1, known: true, resets: []switcher.UsageReset{{HasExpiry: true}}, want: "Expiry unknown"},
		{name: "unsorted dates", count: 3, known: true, resets: []switcher.UsageReset{
			{HasExpiry: true, ExpiresAt: now.Add(7 * 24 * time.Hour)},
			{},
			{HasExpiry: true, ExpiresAt: now.Add(52 * time.Hour)},
		}, want: "Expires in 2d 4h"},
		{name: "hours", count: 1, known: true, resets: []switcher.UsageReset{{HasExpiry: true, ExpiresAt: now.Add(95 * time.Minute)}}, want: "Expires in 1h 35m"},
		{name: "minutes", count: 1, known: true, resets: []switcher.UsageReset{{HasExpiry: true, ExpiresAt: now.Add(5 * time.Minute)}}, want: "Expires in 5m"},
		{name: "under a minute", count: 1, known: true, resets: []switcher.UsageReset{{HasExpiry: true, ExpiresAt: now.Add(30 * time.Second)}}, want: "Expires in <1m"},
		{name: "at expiry", count: 1, known: true, resets: []switcher.UsageReset{{HasExpiry: true, ExpiresAt: now}}, want: "Expired"},
		{name: "past expiry", count: 1, known: true, resets: []switcher.UsageReset{{HasExpiry: true, ExpiresAt: now.Add(-time.Hour)}}, want: "Expired"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := switcher.AccountUsage{ResetCount: test.count, ResetCountKnown: test.known, Resets: test.resets}
			if got := usageResetExpiry(result, now); got != test.want {
				t.Fatalf("expiry = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDashboardKeepsResetExpiryVisibleAtDifferentSizes(t *testing.T) {
	for _, width := range []int{60, 80, 120, 140} {
		m := dashboardModel(2)
		m.width, m.height = width, 24
		for i := range m.usageResults {
			m.usageResults[i].Resets = []switcher.UsageReset{{HasExpiry: true, ExpiresAt: time.Now().Add(52*time.Hour + 30*time.Minute)}}
		}
		view := m.View().Content
		if lipgloss.Width(view) != width || lipgloss.Height(view) != 24 {
			t.Fatalf("%d-column view exceeds terminal: %dx%d", width, lipgloss.Width(view), lipgloss.Height(view))
		}
		if strings.Count(view, "2d 4h") != 2 || !strings.Contains(view, "Expires") || !strings.Contains(view, "quit") {
			t.Fatalf("%d-column view hides expiry or actions", width)
		}
	}
}

func TestDashboardShowsUsageAndSwitchTogetherAtDifferentSizes(t *testing.T) {
	for _, size := range []struct{ width, height int }{{60, 24}, {80, 24}, {120, 24}, {140, 40}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			m := dashboardModel(2)
			m.width, m.height = size.width, size.height
			view := m.View().Content
			if lipgloss.Width(view) != size.width || lipgloss.Height(view) != size.height {
				t.Fatalf("view exceeds terminal: %dx%d", lipgloss.Width(view), lipgloss.Height(view))
			}
			for _, text := range []string{"Account 0", "Account 1", "auth file", "person-0@example.invalid", "plus", "80%", "40%", "22 Oct 14:30", "3 available resets", "switch", "refresh", "more"} {
				if !strings.Contains(view, text) {
					t.Errorf("dashboard missing %q", text)
				}
			}
		})
	}
}

func TestDashboardScrollKeepsSelectionAndActionsVisible(t *testing.T) {
	for _, width := range []int{60, 80, 120} {
		m := dashboardModel(12)
		m.width, m.height, m.cursor = width, 24, 9
		view := m.View().Content
		if lipgloss.Width(view) != width || lipgloss.Height(view) != 24 {
			t.Fatalf("%d-column view exceeds terminal: %dx%d", width, lipgloss.Width(view), lipgloss.Height(view))
		}
		for _, text := range []string{"› ", "Account 9", "of 12", "switch", "quit"} {
			if !strings.Contains(view, text) {
				t.Fatalf("%d-column scrolled view missing %q", width, text)
			}
		}
	}
}

func TestDashboardDoesNotTreatUnknownOrFailedReadsAsZero(t *testing.T) {
	m := dashboardModel(4)
	m.usageResults[1].ResetCountKnown = false
	m.usageResults[2] = switcher.AccountUsage{ProfileID: "account-2", Error: "synthetic unavailable status"}
	m.usageResults = m.usageResults[:3]
	m.usageLoading = true
	m.usageRead = map[string]bool{"account-0": true}
	view := m.renderDashboard(116, 40)
	for _, text := range []string{"2/4 accounts checked", "1 unavailable", "1 available resets (1/4 accounts)", "Loading", "Status unavailable", "Refreshing"} {
		if !strings.Contains(view, text) {
			t.Errorf("partial results missing %q", text)
		}
	}
	for _, i := range []int{2, 3} {
		if strings.Contains(m.renderAccountUsage(i, m.profiles[i], 116), "%") {
			t.Fatal("unavailable rows display invented quota values")
		}
	}
}

func TestUsageResultsArriveOnMainScreenAndSurviveDetails(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m := dashboardModel(2)
	m.cursor = 1
	m.usageResults = nil
	m.usageContext, m.usageCancel = ctx, cancel
	m.usageRead = make(map[string]bool)
	m.usageGeneration, m.usageLoading = 3, true
	row := switcher.AccountUsage{ProfileID: "account-1", Plan: "pro", Windows: []switcher.UsageWindow{{Used: 30}}}
	_, cmd := m.Update(usageResultMsg{generation: 3, result: row})
	if cmd == nil || !m.usageLoading || len(m.usageResults) != 1 || m.mode != modeList {
		t.Fatal("first account was not displayed before the whole read finished")
	}
	m.mode = modeMore
	for i, option := range m.moreOptions() {
		if option.action == moreDetails {
			m.moreCursor = i
		}
	}
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.mode != modeUsageDetail || !strings.Contains(m.renderUsageDetail(68), "Account 1") {
		t.Fatal("More did not open details for the selected switcher account")
	}
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.mode != modeMore || m.cursor != 1 || len(m.usageResults) != 1 || !m.usageLoading {
		t.Fatal("returning from details lost the More menu, selection, results, or running read")
	}
	m.cancelUsage()
	_, cmd = m.Update(usageResultMsg{generation: 3, result: switcher.AccountUsage{ProfileID: "account-1", Plan: "stale"}})
	if cmd != nil || m.usageResults[0].Plan != "pro" {
		t.Fatal("a canceled read overwrote a newer snapshot")
	}
}

func TestUsageDoesNotBlockSwitchOrLeakWaitersOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	m := dashboardModel(2)
	m.usageCancel, m.usageContext, m.usageLoading = cancel, ctx, true
	wait := waitUsage(ctx, m.usageGeneration, make(chan switcher.AccountUsage))
	_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if cmd == nil || m.mode != modeBusy || m.usageLoading || ctx.Err() == nil {
		t.Fatal("switch did not proceed while canceling the outstanding usage read")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- wait() }()
	select {
	case message := <-done:
		if !message.(usageResultMsg).done {
			t.Fatal("canceled status waiter returned a row")
		}
	case <-time.After(time.Second):
		t.Fatal("status waiter remained blocked after cancellation")
	}
}

func TestStartupLoadsStatusAndShutdownDrainsReader(t *testing.T) {
	home := t.TempDir()
	m := model{store: switcher.NewStore(home), width: 80, height: 24}
	for i := range 2 {
		// Valid synthetic identities with unreadable access-token expiry never
		// start a Codex subprocess or make a network request.
		auth := fmt.Sprintf(`{"auth_mode":"chatgpt","last_refresh":"2026-10-21T00:00:00Z","tokens":{"id_token":"header.eyJzdWIiOiJzeW50aGV0aWMifQ.signature","access_token":"synthetic","refresh_token":"synthetic","account_id":"account-%d"}}`, i)
		if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(auth), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := m.store.SaveCurrent(fmt.Sprintf("Account %d", i), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.refresh(); err != nil {
		t.Fatal(err)
	}
	defer m.stopUsage()
	cmd := m.Init()
	if cmd == nil || !m.usageLoading {
		t.Fatal("opening the switcher did not start reading account status")
	}
	for cmd != nil {
		_, cmd = m.Update(cmd())
	}
	if m.mode != modeList || m.usageLoading || len(m.usageResults) != 2 {
		t.Fatal("startup did not deliver both results to the main switcher")
	}
	_ = m.refreshUsage()
	_ = m.quit()
	done := make(chan struct{})
	go func() {
		m.usageWorkers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("quitting left a status reader blocked while sending a result")
	}
}

func TestMoreContainsUsageDetailsAndAddAccount(t *testing.T) {
	m := model{profiles: []switcher.Profile{{ID: "account-a", Name: "Personal"}}}
	options := m.moreOptions()
	var detailsIndex, addIndex = -1, -1
	for i, option := range options {
		switch option.action {
		case moreDetails:
			detailsIndex = i
		case moreAdd:
			addIndex = i
		}
	}
	if detailsIndex < 0 || addIndex < 0 {
		t.Fatal("More does not offer account usage details and account addition")
	}
	m.mode, m.moreCursor = modeMore, addIndex
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.mode != modeAddName {
		t.Fatal("More did not open the add-account name prompt")
	}
	mainModel := dashboardModel(1)
	main := mainModel.renderHelp(80)
	if strings.Contains(main, " add") || strings.Contains(main, "details") {
		t.Fatal("main account screen still shows Add or direct Usage shortcuts")
	}
}
