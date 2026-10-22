package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"codex-account-switcher/internal/switcher"
)

type mode int

const (
	modeList mode = iota
	modeMore
	modeName
	modeRename
	modeDelete
	modeReplace
	modeRestore
	modeRestart
	modeBusy
	modeAddName
	modeLoginConfirm
	modeUsageDetail
)

type switchFinishedMsg struct {
	id      string
	restore bool
	result  switcher.SwitchResult
	err     error
}

type loginExitedMsg struct {
	session *switcher.Login
	name    string
	id      string
	err     error
}

type loginFinishedMsg struct {
	id     string
	result switcher.SwitchResult
	err    error
}

type usageResultMsg struct {
	generation uint64
	result     switcher.AccountUsage
	done       bool
}

type moreAction int

const (
	moreRename moreAction = iota
	moreDelete
	moreRestore
	moreRenew
	moreDetails
	moreAdd
)

type moreOption struct {
	action      moreAction
	title       string
	description string
}

type model struct {
	store           *switcher.Store
	profiles        []switcher.Profile
	activeID        string
	activeExists    bool
	hasRecovery     bool
	width           int
	height          int
	cursor          int
	moreCursor      int
	mode            mode
	input           string
	pendingSwitch   string
	pendingProfile  switcher.Profile
	notice          string
	errorText       string
	queuedCmd       tea.Cmd
	busyText        string
	usageRead       map[string]bool
	usageResults    []switcher.AccountUsage
	usageLoading    bool
	usageCancel     context.CancelFunc
	usageContext    context.Context
	usageUpdates    <-chan switcher.AccountUsage
	usageWorkers    *sync.WaitGroup
	usageGeneration uint64
}

func Run(store *switcher.Store) error {
	m := model{store: store}
	if err := m.refresh(); err != nil {
		return err
	}
	defer m.stopUsage()
	_, err := tea.NewProgram(&m).Run()
	return err
}

func (m *model) refresh() error {
	profiles, err := m.store.List()
	if err != nil {
		return err
	}
	m.profiles = profiles
	m.activeID, m.activeExists, err = m.store.ActiveID()
	if err != nil {
		m.activeID = ""
		m.activeExists = false
		m.notice = "Cannot identify auth.json. You can restore a valid recovery copy."
	}
	if m.activeID != "" {
		updated, err := m.store.SyncCurrent()
		if err != nil {
			m.errorText = fmt.Sprintf("Cannot update saved login: %v", err)
		} else if updated {
			m.notice = "Saved login updated from current auth.json."
		}
	}
	m.hasRecovery, err = m.store.HasRecovery()
	if err != nil {
		m.hasRecovery = false
		m.notice = "The recovery copy is invalid or unavailable."
	}
	if m.cursor >= len(m.profiles) {
		m.cursor = len(m.profiles) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.moreCursor >= len(m.moreOptions()) {
		m.moreCursor = max(len(m.moreOptions())-1, 0)
	}
	return nil
}

func (m *model) canSaveCurrent() bool {
	return m.activeExists && m.activeID == ""
}

func (m *model) moreOptions() []moreOption {
	var options []moreOption
	if len(m.profiles) > 0 {
		options = append(options,
			moreOption{action: moreRename, title: "Rename profile", description: "Change its name. The saved login stays the same."},
			moreOption{action: moreDelete, title: "Delete profile", description: "Remove its saved credentials. The active Codex login stays unchanged."},
		)
	}
	if m.hasRecovery {
		options = append(options, moreOption{
			action:      moreRestore,
			title:       "Restore previous login",
			description: "Restore the login saved before the last switch. A recognized current account is saved to its profile first.",
		})
	}
	if len(m.profiles) > 0 {
		options = append(options,
			moreOption{action: moreRenew, title: "Renew login", description: "Sign in again to this same account in a separate login session. If it is active, reload Codex after confirmation."},
			moreOption{action: moreDetails, title: "Account usage details", description: "View this account’s complete quota windows, reset credits, and expiration times."},
		)
	}
	options = append(options, moreOption{action: moreAdd, title: "Add account", description: "Sign in to another account and save it without changing the current Codex login."})
	return options
}

func (m *model) Init() tea.Cmd { return m.refreshUsage() }

func (m *model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if exited, ok := message.(loginExitedMsg); ok {
		m.mode = modeBusy
		m.busyText = "Saving login…"
		store := m.store
		return m, func() tea.Msg {
			result, err := exited.session.Complete(store, exited.name, exited.id, exited.err)
			return loginFinishedMsg{id: exited.id, result: result, err: err}
		}
	}
	if finished, ok := message.(loginFinishedMsg); ok {
		m.mode = modeList
		m.pendingProfile = switcher.Profile{}
		m.input = ""
		if finished.err != nil {
			m.setError(finished.err)
		} else if finished.id == "" {
			m.notice = "Account added. Select it and press Enter to switch."
		} else if finished.result.ServerVerified {
			m.notice = "Login renewed and loaded by Codex. Reopen disconnected clients."
		} else {
			m.notice = "Saved login renewed. Select the profile to use it in Codex."
		}
		m.refreshAfterAction()
		return m, m.refreshUsage()
	}
	if finished, ok := message.(usageResultMsg); ok {
		if finished.generation != m.usageGeneration {
			return m, nil
		}
		if finished.done {
			m.usageLoading = false
			m.usageCancel = nil
			return m, nil
		}
		m.usageRead[finished.result.ProfileID] = true
		for i, row := range m.usageResults {
			if row.ProfileID == finished.result.ProfileID {
				m.usageResults[i] = finished.result
				return m, waitUsage(m.usageContext, m.usageGeneration, m.usageUpdates)
			}
		}
		m.usageResults = append(m.usageResults, finished.result)
		return m, waitUsage(m.usageContext, m.usageGeneration, m.usageUpdates)
	}
	if finished, ok := message.(switchFinishedMsg); ok {
		m.finishSwitch(finished)
		if m.mode == modeList {
			return m, m.refreshUsage()
		}
		return m, nil
	}
	if size, ok := message.(tea.WindowSizeMsg); ok {
		m.width = size.Width
		m.height = size.Height
		return m, nil
	}
	keyMessage, ok := message.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	key := keyMessage.String()
	if m.mode == modeBusy {
		return m, nil
	}
	if key == "ctrl+c" {
		return m, m.quit()
	}
	if m.mode == modeUsageDetail {
		switch key {
		case "esc":
			m.mode = modeMore
		case "r":
			if !m.usageLoading {
				return m, m.refreshUsage()
			}
		}
		return m, nil
	}
	switch m.mode {
	case modeAddName:
		m.updateAddName(keyMessage)
	case modeLoginConfirm:
		switch key {
		case "esc", "n", "N":
			m.mode = modeList
			m.input = ""
			m.pendingProfile = switcher.Profile{}
			m.notice = "Login canceled."
		case "y", "Y", "enter":
			return m, m.startLogin()
		}
	case modeRestart:
		switch key {
		case "esc", "n", "N":
			m.mode = modeList
			m.pendingSwitch = ""
			m.notice = "Switch canceled. Login was not changed."
		case "y", "Y", "enter":
			m.queueSwitch(m.pendingSwitch, false, true)
		}
	case modeMore:
		switch key {
		case "esc":
			m.mode = modeList
		case "up", "k":
			if m.moreCursor > 0 {
				m.moreCursor--
			}
		case "down", "j":
			if m.moreCursor < len(m.moreOptions())-1 {
				m.moreCursor++
			}
		case "enter":
			options := m.moreOptions()
			if len(options) > 0 {
				selected := options[m.moreCursor]
				switch selected.action {
				case moreRename:
					m.pendingProfile = m.profiles[m.cursor]
					m.input = ""
					m.errorText = ""
					m.mode = modeRename
				case moreDelete:
					m.pendingProfile = m.profiles[m.cursor]
					m.errorText = ""
					m.mode = modeDelete
				case moreRenew:
					m.pendingProfile = m.profiles[m.cursor]
					m.input = ""
					m.errorText = ""
					m.notice = ""
					m.mode = modeLoginConfirm
				case moreRestore:
					m.mode = modeRestore
				case moreDetails:
					m.mode = modeUsageDetail
				case moreAdd:
					m.mode = modeAddName
					m.input = ""
					m.errorText = ""
					m.notice = ""
					m.pendingProfile = switcher.Profile{}
				}
			}
		case "q":
			return m, m.quit()
		}
	case modeName:
		m.updateName(keyMessage)
	case modeRename:
		m.updateRename(keyMessage)
	case modeDelete:
		switch key {
		case "esc", "n", "N":
			m.mode = modeMore
			m.errorText = ""
			m.notice = "Delete canceled."
		case "y", "Y", "enter":
			m.cancelUsage()
			if err := m.store.DeleteProfile(m.pendingProfile.ID); err != nil {
				m.errorText = err.Error()
				m.notice = ""
				return m, nil
			}
			m.mode = modeList
			m.notice = fmt.Sprintf("Profile %q was deleted. The active login was not changed.", m.pendingProfile.Name)
			m.pendingProfile = switcher.Profile{}
			m.refreshAfterAction()
			m.queuedCmd = m.refreshUsage()
		case "q":
			return m, m.quit()
		}
	case modeReplace:
		switch key {
		case "esc", "n", "N":
			m.mode = modeList
			m.pendingSwitch = ""
			m.notice = "Save canceled."
		case "y", "Y", "enter":
			if _, err := m.store.SaveCurrent(m.input, true); err != nil {
				m.setError(err)
				return m, nil
			}
			m.mode = modeList
			m.notice = "Current login saved."
			if m.pendingSwitch != "" {
				m.switchTo(m.pendingSwitch)
			}
			m.pendingSwitch = ""
			m.refreshAfterAction()
			if m.mode == modeList {
				m.queuedCmd = m.refreshUsage()
			}
		case "q":
			return m, m.quit()
		}
	case modeRestore:
		switch key {
		case "esc", "n", "N":
			m.mode = modeMore
			m.errorText = ""
			m.notice = "Restore canceled."
		case "y", "Y", "enter":
			m.queueSwitch("", true, true)
		case "q":
			return m, m.quit()
		}
	default:
		switch key {
		case "q", "esc":
			return m, m.quit()
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.profiles)-1 {
				m.cursor++
			}
		case "s":
			if m.canSaveCurrent() {
				m.mode = modeName
				m.input = ""
				m.pendingSwitch = ""
				m.errorText = ""
			}
		case "m":
			m.mode = modeMore
			m.errorText = ""
		case "enter", " ":
			if len(m.profiles) > 0 {
				m.switchTo(m.profiles[m.cursor].ID)
				m.refreshAfterAction()
			}
		case "u":
			if len(m.profiles) > 0 {
				m.mode = modeUsageDetail
			}
		case "r":
			if !m.usageLoading {
				return m, m.refreshUsage()
			}
		}
	}
	cmd := m.queuedCmd
	m.queuedCmd = nil
	return m, cmd
}

func (m *model) refreshUsage() tea.Cmd {
	m.cancelUsage()
	if len(m.profiles) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	generation := m.usageGeneration
	updates := make(chan switcher.AccountUsage)
	m.usageContext = ctx
	m.usageUpdates = updates
	m.usageCancel = cancel
	m.usageLoading = true
	m.usageRead = make(map[string]bool)
	store := m.store
	if m.usageWorkers == nil {
		m.usageWorkers = &sync.WaitGroup{}
	}
	workers := m.usageWorkers
	workers.Add(1)
	go func() {
		defer workers.Done()
		defer close(updates)
		defer cancel()
		store.ReadUsage(ctx, func(row switcher.AccountUsage) {
			select {
			case updates <- row:
			case <-ctx.Done():
			}
		})
	}()
	return waitUsage(ctx, generation, updates)
}

func waitUsage(ctx context.Context, generation uint64, updates <-chan switcher.AccountUsage) tea.Cmd {
	return func() tea.Msg {
		select {
		case row, ok := <-updates:
			return usageResultMsg{generation: generation, result: row, done: !ok}
		case <-ctx.Done():
			return usageResultMsg{generation: generation, done: true}
		}
	}
}

// Wait after the TUI exits so child cleanup and temporary-file removal finish
// before main can terminate the process.
func (m *model) stopUsage() {
	m.cancelUsage()
	if m.usageWorkers != nil {
		m.usageWorkers.Wait()
	}
}

func (m *model) quit() tea.Cmd {
	m.cancelUsage()
	return tea.Quit
}

func (m *model) cancelUsage() {
	m.usageGeneration++
	if m.usageCancel != nil {
		m.usageCancel()
		m.usageCancel = nil
	}
	m.usageLoading = false
}

func (m *model) updateName(key tea.KeyPressMsg) {
	switch key.String() {
	case "esc":
		m.mode = modeList
		m.pendingSwitch = ""
		m.notice = "Save canceled."
	case "enter":
		if strings.TrimSpace(m.input) == "" {
			m.errorText = "Enter a profile name."
			return
		}
		_, err := m.store.SaveCurrent(m.input, false)
		if errors.Is(err, switcher.ErrProfileExists) {
			m.mode = modeReplace
			m.errorText = ""
			return
		}
		if err != nil {
			if errors.Is(err, switcher.ErrNameTaken) {
				m.errorText = "This name belongs to another account. Choose a different name."
				return
			}
			if errors.Is(err, switcher.ErrProfileNameDiff) {
				m.errorText = "This account is already saved under another name. Use that name to update it."
				return
			}
			m.setError(err)
			return
		}
		m.mode = modeList
		m.notice = "Current login saved."
		pending := m.pendingSwitch
		m.pendingSwitch = ""
		if pending != "" {
			m.switchTo(pending)
		}
		m.refreshAfterAction()
		if m.mode == modeList {
			m.queuedCmd = m.refreshUsage()
		}
	default:
		m.editNameInput(key)
	}
}

func (m *model) updateRename(key tea.KeyPressMsg) {
	switch key.String() {
	case "esc":
		m.mode = modeMore
		m.input = ""
		m.errorText = ""
	case "enter":
		if strings.TrimSpace(m.input) == "" {
			m.errorText = "Enter a profile name."
			return
		}
		if err := m.store.RenameProfile(m.pendingProfile.ID, m.input); err != nil {
			m.errorText = err.Error()
			return
		}
		m.mode = modeList
		m.notice = fmt.Sprintf("Profile %q was renamed.", m.pendingProfile.Name)
		m.pendingProfile = switcher.Profile{}
		m.input = ""
		m.refreshAfterAction()
	default:
		m.editNameInput(key)
	}
}

func (m *model) switchTo(id string) {
	m.queueSwitch(id, false, false)
}

func (m *model) queueSwitch(id string, restore, allowRestart bool) {
	m.cancelUsage()
	m.mode = modeBusy
	m.busyText = "Switching account…"
	m.notice = ""
	m.errorText = ""
	store := m.store
	m.queuedCmd = func() tea.Msg {
		var result switcher.SwitchResult
		var err error
		if restore {
			result, err = store.RestoreAccount(allowRestart)
		} else {
			result, err = store.SwitchAccount(id, allowRestart)
		}
		return switchFinishedMsg{id: id, restore: restore, result: result, err: err}
	}
}

func (m *model) finishSwitch(finished switchFinishedMsg) {
	if errors.Is(finished.err, switcher.ErrServerRestartRequired) {
		m.mode = modeRestart
		m.pendingSwitch = finished.id
		return
	}
	if errors.Is(finished.err, switcher.ErrUnknownCurrent) {
		m.pendingSwitch = finished.id
		m.mode = modeName
		m.input = ""
		m.notice = "Save the current unrecognized account under a new name first."
		return
	}
	if finished.err != nil {
		m.setError(finished.err)
		m.refreshAfterAction()
		return
	}
	m.mode = modeList
	m.pendingSwitch = ""
	if finished.result.ServerVerified {
		m.notice = "Account switched and confirmed by Codex. Reopen disconnected clients."
		if finished.result.Already {
			m.notice = "This account is already selected in Codex. Current tokens saved."
		}
	} else {
		m.notice = "Login file selected. Codex server is stopped; start Codex to use this account."
	}
	m.refreshAfterAction()
}

func (m *model) refreshAfterAction() {
	if m.mode == modeBusy {
		return
	}
	if err := m.refresh(); err != nil {
		m.setError(err)
	}
}

func (m *model) setError(err error) {
	m.mode = modeList
	m.errorText = err.Error()
	m.notice = ""
}

func (m *model) View() tea.View {
	width := m.width
	if width < 1 {
		width = 80
	}
	height := m.height
	if height < 1 {
		height = 24
	}
	maxWidth := 68
	if m.mode == modeList {
		maxWidth = 116
	}
	contentWidth := min(width-4, maxWidth)
	if contentWidth < 20 {
		contentWidth = max(width, 1)
	}

	help := m.renderHelp(contentWidth)
	feedback := m.renderFeedback(contentWidth)
	listHeight := height - 3 - lipgloss.Height(help)
	if feedback != "" {
		listHeight -= lipgloss.Height(feedback)
	}
	sections := []string{m.renderHeader(), ""}
	switch m.mode {
	case modeMore:
		sections = append(sections, m.renderMore(contentWidth))
	case modeUsageDetail:
		sections = append(sections, m.renderUsageDetail(contentWidth))
	case modeRename, modeDelete, modeRestore, modeRestart, modeBusy, modeAddName, modeLoginConfirm:
		sections = append(sections, m.renderAction(contentWidth))
	case modeList:
		sections = append(sections, m.renderDashboard(contentWidth, listHeight))
	default:
		sections = append(sections, m.renderProfiles(contentWidth))
		if action := m.renderAction(contentWidth); action != "" {
			sections = append(sections, "", action)
		}
	}
	if feedback != "" {
		sections = append(sections, feedback)
	}
	sections = append(sections, "", help)
	content := lipgloss.JoinVertical(lipgloss.Left, sections...)
	content = lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, content)

	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "Codex Switch"
	return view
}

func (m *model) renderHeader() string {
	return lipgloss.NewStyle().Bold(true).Render("Codex Switch")
}

func (m *model) renderProfiles(width int) string {
	label := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("Accounts")
	rows := []string{label}
	if len(m.profiles) == 0 {
		message := "No saved accounts yet."
		if !m.activeExists {
			message = "No readable current login."
		}
		rows = append(rows, lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(message))
		return lipgloss.JoinVertical(lipgloss.Left, rows...)
	}

	for i, profile := range m.profiles {
		rows = append(rows, lipgloss.NewStyle().Width(width).Render(m.accountName(i, profile, width)))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m *model) renderMore(width int) string {
	label := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("More options")
	rows := []string{label}
	if len(m.profiles) > 0 {
		profile := m.profiles[m.cursor]
		name := lipgloss.NewStyle().Bold(true).Render(profile.Name)
		muted := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
		rows = append(rows,
			muted.Render("Profile: ")+name,
			muted.Render("Profile ID: ")+profile.ID,
		)
	}
	options := m.moreOptions()
	if len(options) == 0 {
		rows = append(rows, "", lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("No additional options are available."))
		return lipgloss.JoinVertical(lipgloss.Left, rows...)
	}
	selectedIndex := min(max(m.moreCursor, 0), len(options)-1)
	for i, option := range options {
		marker := "  "
		name := option.title
		if i == selectedIndex {
			marker = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true).Render("› ")
			name = lipgloss.NewStyle().Bold(true).Render(name)
		}
		rows = append(rows, marker+name)
	}
	if len(options) > 0 {
		rows = append(rows, "", lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Width(max(width-2, 1)).
			Render(options[selectedIndex].description))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m *model) renderAction(width int) string {
	switch m.mode {
	case modeBusy:
		text := m.busyText
		if text == "" {
			text = "Switching account…"
		}
		return text + "\nPlease wait while the login and Codex server are checked."
	case modeAddName:
		return lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Bold(true).Render("ADD ACCOUNT"),
			"Choose a short name for the new account.",
			lipgloss.NewStyle().Width(width).Render("  "+m.input+"▏"))
	case modeLoginConfirm:
		title := "ADD ACCOUNT?"
		details := "Sign in through the Codex device login shown next. Open the link in your browser and choose the account you want to add. Your current Codex login stays unchanged."
		if m.pendingProfile.ID != "" {
			title = "RENEW LOGIN?"
			details = fmt.Sprintf("Sign in to the same account as %q. A different account will be rejected. If this profile is active, the shared Codex server will restart to load the new login. Finish other sessions first; their work may be interrupted.", m.pendingProfile.Name)
		}
		return lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Bold(true).Render(title),
			lipgloss.NewStyle().Width(width).Render(details), "",
			lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Width(width).Render("The login uses an empty private Codex home. Enable device code authorization in ChatGPT security settings if prompted. Ctrl+C cancels the login; it expires after 10 minutes."))
	case modeRestart:
		name := "the selected profile"
		for _, profile := range m.profiles {
			if profile.ID == m.pendingSwitch {
				name = fmt.Sprintf("%q", profile.Name)
				break
			}
		}
		return lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Bold(true).Render("RESTART CODEX AND SWITCH?"),
			lipgloss.NewStyle().Width(width).Render("Switch to "+name+" and reload its login in Codex."),
			"",
			lipgloss.NewStyle().Width(width).Render("Finish work in other Codex sessions first. The shared server will stop, which can interrupt their work. Reopen clients after the switch."),
		)
	case modeName:
		label := lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true).Render("SAVE CURRENT LOGIN")
		prompt := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("Choose a short label for this account")
		input := lipgloss.NewStyle().Bold(true).Render(m.input + "▏")
		return lipgloss.JoinVertical(lipgloss.Left, label, prompt,
			lipgloss.NewStyle().Width(width).Render("  "+input),
			lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("Enter save  ·  Esc cancel"),
		)
	case modeReplace:
		label := lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true).Render("UPDATE SAVED ACCOUNT")
		question := fmt.Sprintf("Replace %q with the current login?", m.input)
		return lipgloss.JoinVertical(lipgloss.Left, label, question,
			lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("y confirm  ·  n cancel"),
		)
	case modeRename:
		label := lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true).Render("RENAME PROFILE")
		current := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("Current name: ") + m.pendingProfile.Name
		prompt := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("Enter a new name")
		input := lipgloss.NewStyle().Bold(true).Render(m.input + "▏")
		return lipgloss.JoinVertical(lipgloss.Left, label, current, prompt,
			lipgloss.NewStyle().Width(width).Render("  "+input),
		)
	case modeDelete:
		label := lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true).Render("DELETE PROFILE?")
		name := fmt.Sprintf("Delete %q and its saved credentials?", m.pendingProfile.Name)
		details := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Width(width).
			Render("This does not sign out of Codex or change the active auth.json.")
		return lipgloss.JoinVertical(lipgloss.Left, label, name, details)
	case modeRestore:
		label := lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true).Render("RESTORE PREVIOUS LOGIN?")
		return lipgloss.JoinVertical(lipgloss.Left, label,
			"This restores the login saved before the last switch.",
			lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Width(width).
				Render("A recognized current account is saved to its profile first. Use this to recover from an unwanted switch."),
			"",
			lipgloss.NewStyle().Width(width).Render("If the shared Codex server is running, it will restart. Finish other sessions first; their work may be interrupted."),
		)
	default:
		return ""
	}
}

func (m *model) renderFeedback(width int) string {
	if m.errorText != "" {
		text := lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true).Render("!  " + m.errorText)
		return lipgloss.NewStyle().Width(width).Render(text)
	}
	if m.notice != "" {
		text := lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Render("•  " + m.notice)
		return lipgloss.NewStyle().Width(width).Render(text)
	}
	return ""
}

func (m *model) renderHelp(width int) string {
	key := func(name, description string) string {
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")).Render(name) +
			lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(" "+description)
	}
	var hints []string
	switch m.mode {
	case modeBusy:
		return ""
	case modeMore:
		hints = []string{key("↑↓", "move"), key("enter", "open"), key("esc", "back"), key("q", "quit")}
		if len(m.moreOptions()) == 0 {
			hints = []string{key("esc", "back"), key("q", "quit")}
		}
	case modeUsageDetail:
		hints = []string{key("r", "refresh all"), key("esc", "back")}
	case modeName, modeAddName:
		hints = []string{key("enter", "save"), key("esc", "cancel")}
	case modeRename:
		hints = []string{key("enter", "rename"), key("esc", "cancel")}
	case modeReplace, modeRestore, modeRestart, modeLoginConfirm:
		hints = []string{key("y", "confirm"), key("esc", "cancel")}
	case modeDelete:
		hints = []string{key("y", "delete"), key("esc", "cancel")}
	default:
		hints = []string{key("↑↓", "move")}
		if len(m.profiles) > 0 {
			hints = append(hints, key("enter", "switch"))
		}
		if m.canSaveCurrent() {
			hints = append(hints, key("s", "save current"))
		}
		hints = append(hints, key("m", "more"))
		if len(m.profiles) > 0 {
			hints = append(hints, key("r", "refresh"))
		}
		hints = append(hints, key("q", "quit"))
	}
	line := strings.Join(hints, "   ")
	if lipgloss.Width(line) > width {
		line = ""
		lineWidth := 0
		for _, hint := range hints {
			if lineWidth > 0 {
				if lineWidth+3+lipgloss.Width(hint) > width {
					line += "\n"
					lineWidth = 0
				} else {
					line += "   "
					lineWidth += 3
				}
			}
			line += hint
			lineWidth += lipgloss.Width(hint)
		}
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(line)
}

func (m *model) updateAddName(key tea.KeyPressMsg) {
	switch key.String() {
	case "esc":
		m.mode = modeList
		m.input = ""
		m.errorText = ""
	case "enter":
		name := strings.TrimSpace(m.input)
		if name == "" {
			m.errorText = "Enter a profile name."
			return
		}
		for _, profile := range m.profiles {
			if profile.Name == name {
				m.errorText = "This name is already used. Choose another name."
				return
			}
		}
		m.input = name
		m.mode = modeLoginConfirm
		m.errorText = ""
	default:
		m.editNameInput(key)
	}
}

func (m *model) startLogin() tea.Cmd {
	m.cancelUsage()
	session, err := switcher.NewLogin()
	if err != nil {
		m.setError(err)
		return nil
	}
	name, id := m.input, m.pendingProfile.ID
	m.mode = modeBusy
	m.busyText = "Waiting for Codex login…"
	m.notice = ""
	m.errorText = ""
	return tea.ExecProcess(session.Command, func(err error) tea.Msg {
		return loginExitedMsg{session: session, name: name, id: id, err: err}
	})
}

func (m *model) editNameInput(key tea.KeyPressMsg) {
	switch key.String() {
	case "backspace", "delete":
		if m.input != "" {
			_, size := utf8.DecodeLastRuneInString(m.input)
			m.input = m.input[:len(m.input)-size]
		}
	default:
		text := key.Key().Text
		if text != "" && utf8.RuneCountInString(m.input)+utf8.RuneCountInString(text) <= 40 && !strings.ContainsAny(text, "\r\n\t") {
			m.input += text
			m.errorText = ""
		}
	}
}
