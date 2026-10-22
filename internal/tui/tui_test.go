package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"codex-account-switcher/internal/switcher"
)

func TestViewUsesFullScreenAndShowsAccountActions(t *testing.T) {
	testModel := model{
		profiles: []switcher.Profile{
			{ID: "account-a", Name: "Personal"},
			{ID: "account-b", Name: "Work"},
		},
		activeID:     "account-a",
		activeExists: true,
		hasRecovery:  true,
		width:        80,
		height:       24,
	}

	view := testModel.View()
	if !view.AltScreen {
		t.Fatal("View did not enable the alternate full-screen buffer")
	}
	if view.WindowTitle != "Codex Switch" {
		t.Fatalf("WindowTitle = %q, want Codex Switch", view.WindowTitle)
	}
	if width := lipgloss.Width(view.Content); width != 80 {
		t.Fatalf("rendered width = %d, want terminal width 80", width)
	}
	if height := lipgloss.Height(view.Content); height != 24 {
		t.Fatalf("rendered height = %d, want terminal height 24", height)
	}
	for _, text := range []string{"Codex Switch", "Accounts", "Personal", "Work", "auth file", "switch", "more"} {
		if !strings.Contains(view.Content, text) {
			t.Errorf("rendered view does not contain %q", text)
		}
	}
	if strings.Contains(view.Content, "restore") || strings.Contains(view.Content, "save current") {
		t.Fatal("main view shows an unavailable or unnecessary action")
	}
	if strings.Contains(view.Content, "CURRENT AUTH FILE") || strings.Contains(view.Content, "Local account manager") {
		t.Fatal("main view contains redundant labels")
	}
}

func TestSaveCurrentHintOnlyShowsForUnknownAccount(t *testing.T) {
	testModel := model{
		profiles:     []switcher.Profile{{ID: "account-a", Name: "Personal"}},
		activeID:     "account-a",
		activeExists: true,
	}
	got := testModel.renderHelp(68)
	if strings.Contains(got, "save current") {
		t.Fatal("help shows save when the active account is already saved")
	}

	testModel.activeID = ""
	got = testModel.renderHelp(68)
	if !strings.Contains(got, "save current") {
		t.Fatal("help does not show save for an unsaved active account")
	}
}

func TestActiveMarkerStaysBesideItsAccount(t *testing.T) {
	testModel := model{
		profiles: []switcher.Profile{
			{ID: "account-a", Name: "Personal"},
			{ID: "account-b", Name: "Work"},
		},
		activeID: "account-a",
		width:    68,
	}

	for _, line := range strings.Split(testModel.renderProfiles(68), "\n") {
		if strings.Contains(line, "Personal") {
			if !strings.Contains(line, "(auth file)") {
				t.Fatal("active marker is not beside the active account")
			}
			return
		}
	}
	t.Fatal("active account row was not rendered")
}

func TestRestartConfirmationDoesNotScheduleWorkOnCancel(t *testing.T) {
	testModel := model{mode: modeRestart, pendingSwitch: "account-b"}
	updated, cmd := testModel.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	got := updated.(*model)
	if cmd != nil || got.mode != modeList || got.pendingSwitch != "" {
		t.Fatal("canceling restart scheduled work or retained the pending switch")
	}
}

func TestSwitchWaitsForConfirmationAndIgnoresKeysDuringWork(t *testing.T) {
	testModel := model{mode: modeBusy}
	_, cmd := testModel.Update(switchFinishedMsg{id: "account-b", err: switcher.ErrServerRestartRequired})
	if cmd != nil || testModel.mode != modeRestart {
		t.Fatal("restart was not presented as a separate confirmation")
	}
	_, cmd = testModel.Update(tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}))
	if cmd == nil || testModel.mode != modeBusy {
		t.Fatal("confirming restart did not schedule the switch")
	}
	for _, key := range []tea.Key{{Code: tea.KeyEscape}, {Code: 'q', Text: "q"}, {Code: 'c', Mod: tea.ModCtrl}} {
		_, cmd = testModel.Update(tea.KeyPressMsg(key))
		if cmd != nil || testModel.mode != modeBusy {
			t.Fatal("key input interrupted the switch transaction")
		}
	}
}

func TestMoreMenuExplainsRestoreOnlyWhenAvailable(t *testing.T) {
	testModel := model{
		mode:        modeMore,
		hasRecovery: true,
		profiles:    []switcher.Profile{{ID: "account-a", Name: "Personal"}},
		width:       80,
		height:      24,
	}
	for i, option := range testModel.moreOptions() {
		if option.action == moreRestore {
			testModel.moreCursor = i
		}
	}
	view := testModel.View()
	if height := lipgloss.Height(view.Content); height > 24 {
		t.Fatalf("More view exceeds the terminal: %d rows", height)
	}
	for _, text := range []string{"More options", "Profile ID:", "account-a", "Restore previous login", "last switch", "saved to its profile first"} {
		if !strings.Contains(view.Content, text) {
			t.Errorf("More view does not contain %q", text)
		}
	}
	if strings.Contains(view.Content, "Accounts") {
		t.Fatal("More view also shows the main account list")
	}

	testModel.hasRecovery = false
	testModel.profiles = nil
	view = testModel.View()
	if !strings.Contains(view.Content, "Add account") {
		t.Fatal("More view does not offer account addition when no profiles are saved")
	}
}

func TestMoreOpensRestoreConfirmation(t *testing.T) {
	testModel := model{hasRecovery: true}
	updated, _ := testModel.Update(tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	moreModel := updated.(*model)
	if moreModel.mode != modeMore {
		t.Fatalf("mode after m = %v, want modeMore", moreModel.mode)
	}

	updated, _ = moreModel.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if got := updated.(*model).mode; got != modeRestore {
		t.Fatalf("mode after opening restore = %v, want modeRestore", got)
	}
}

func TestMoreOpensActionsForSelectedProfile(t *testing.T) {
	testModel := model{
		profiles: []switcher.Profile{
			{ID: "account-a", Name: "Personal"},
			{ID: "account-b", Name: "Work"},
		},
	}
	updated, _ := testModel.Update(tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	moreModel := updated.(*model)
	updated, _ = moreModel.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	moreModel = updated.(*model)
	updated, _ = moreModel.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	deleteModel := updated.(*model)
	if deleteModel.mode != modeDelete || deleteModel.pendingProfile.ID != "account-a" {
		t.Fatalf("selected action = mode %v for profile %q; want delete selected profile account-a", deleteModel.mode, deleteModel.pendingProfile.ID)
	}
}

func TestWindowResizeUpdatesLayout(t *testing.T) {
	testModel := model{}
	updated, _ := testModel.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	resized := updated.(*model)
	if resized.width != 100 || resized.height != 30 {
		t.Fatalf("window size = %dx%d, want 100x30", resized.width, resized.height)
	}
}
