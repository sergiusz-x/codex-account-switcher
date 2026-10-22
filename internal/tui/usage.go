package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"codex-account-switcher/internal/switcher"
)

func (m *model) renderUsageDetail(width int) string {
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	if len(m.profiles) == 0 {
		return "No saved accounts yet."
	}
	profile := m.profiles[min(m.cursor, len(m.profiles)-1)]
	result, found := m.usageResult(profile.ID)
	title := profile.Name
	if found && result.Plan != "" {
		title += " · " + result.Plan
		if !result.PlanFromStatus {
			title += " (saved)"
		}
	}
	rows := []string{lipgloss.NewStyle().Bold(true).Render(title)}
	if found && result.Email != "" {
		rows = append(rows, muted.Width(width).Render(result.Email))
	}
	rows = append(rows, "")
	if !found {
		message := "Not checked. Press r to refresh all accounts."
		if m.usageLoading {
			message = "Loading account status…"
		}
		rows = append(rows, muted.Width(width).Render(message))
		return lipgloss.JoinVertical(lipgloss.Left, rows...)
	}
	if result.Error != "" {
		rows = append(rows, lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Width(width).Render(result.Error))
		return lipgloss.JoinVertical(lipgloss.Left, rows...)
	}
	rows = append(rows, lipgloss.NewStyle().Bold(true).Render("Remaining"))
	if len(result.Windows) == 0 {
		rows = append(rows, muted.Render("Quota windows are unavailable."))
	}
	for _, window := range result.Windows {
		label := usageWindowLabel(window.Duration)
		remaining := 100 - window.Used
		line := fmt.Sprintf("%-12s %3d%%", label, remaining)
		if window.HasResetAt {
			line += "   Resets " + window.ResetsAt.Format("02 Jan 15:04")
		}
		rows = append(rows, line)
	}
	rows = append(rows, "")
	if result.ResetCountKnown {
		rows = append(rows, lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("Available resets · %d", result.ResetCount)))
		if !result.ResetDetailsKnown {
			rows = append(rows, muted.Render("Reset details are unavailable."))
		} else if len(result.Resets) == 0 {
			rows = append(rows, muted.Render("No available reset details."))
		} else {
			maxRows := max(1, min(len(result.Resets), 8))
			for _, reset := range result.Resets[:maxRows] {
				label := reset.Title
				if label == "" {
					label = strings.ReplaceAll(reset.ResetType, "_", " ")
				}
				if label == "" {
					label = "Rate-limit reset"
				}
				if reset.HasExpiry {
					label += " · expires " + reset.ExpiresAt.Format("02 Jan 15:04")
				} else {
					label += " · expiry unknown"
				}
				rows = append(rows, lipgloss.NewStyle().Width(width).Render(label))
			}
			if len(result.Resets) > maxRows {
				rows = append(rows, muted.Render(fmt.Sprintf("And %d more", len(result.Resets)-maxRows)))
			}
		}
	} else {
		rows = append(rows, lipgloss.NewStyle().Bold(true).Render("Available resets"), muted.Render("Count unavailable."))
	}
	if !result.UpdatedAt.IsZero() {
		zone := time.Now().Format("MST -07:00")
		rows = append(rows, "", muted.Render("Times shown in "+zone+" · Updated "+usageAge(result.UpdatedAt)))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m *model) usageResult(profileID string) (switcher.AccountUsage, bool) {
	for _, result := range m.usageResults {
		if result.ProfileID == profileID {
			return result, true
		}
	}
	return switcher.AccountUsage{}, false
}

func usageWindowLabel(duration time.Duration) string {
	if duration == 0 {
		return "Quota"
	}
	if duration%time.Hour == 0 {
		hours := int(duration / time.Hour)
		if hours >= 24 && hours%24 == 0 {
			return fmt.Sprintf("%dd", hours/24)
		}
		return fmt.Sprintf("%dh", hours)
	}
	if duration%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(duration/time.Minute))
	}
	return duration.String()
}

func usageAge(updated time.Time) string {
	d := time.Since(updated).Round(time.Minute)
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
}

// renderDashboard keeps the switch action and every account's quotas together.
// Only the visible account blocks are rendered when the terminal is short.
func (m *model) renderDashboard(width, height int) string {
	if len(m.profiles) == 0 {
		return m.renderProfiles(width)
	}
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	header := "Accounts · limits remaining"
	if width >= 100 {
		accountWidth := width - 66
		header = lipgloss.JoinHorizontal(lipgloss.Top,
			usageCell("Accounts", accountWidth), "  ",
			usageCell("Remaining", 52), "  ", usageCell("Resets", 10))
	}
	rows := []string{lipgloss.NewStyle().Bold(true).Render(header), muted.Render(m.usageSummary(width)), ""}
	blocks := make([]string, len(m.profiles))
	totalHeight := 0
	for i, profile := range m.profiles {
		blocks[i] = m.renderAccountUsage(i, profile, width)
		totalHeight += lipgloss.Height(blocks[i]) + 1
	}
	available := max(1, height-lipgloss.Height(lipgloss.JoinVertical(lipgloss.Left, rows...)))
	scrolling := totalHeight-1 > available
	if scrolling {
		available = max(1, available-1)
	}
	start := min(m.cursor, len(blocks)-1)
	end := start + 1
	used := lipgloss.Height(blocks[start])
	for start > 0 && used+1+lipgloss.Height(blocks[start-1]) <= available {
		start--
		used += 1 + lipgloss.Height(blocks[start])
	}
	for end < len(blocks) && used+1+lipgloss.Height(blocks[end]) <= available {
		used += 1 + lipgloss.Height(blocks[end])
		end++
	}
	body := strings.Join(blocks[start:end], "\n\n")
	rows = append(rows, lipgloss.NewStyle().MaxHeight(available).Render(body))
	if scrolling {
		rows = append(rows, muted.Render(fmt.Sprintf("Accounts %d–%d of %d", start+1, end, len(blocks))))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m *model) renderAccountUsage(index int, profile switcher.Profile, width int) string {
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	result, found := m.usageResult(profile.ID)
	plan := result.Plan
	if plan != "" && !result.PlanFromStatus {
		plan += " (saved)"
	}
	metadata := plan
	if !result.UpdatedAt.IsZero() {
		if metadata != "" {
			metadata += " · "
		}
		metadata += "Updated " + usageAge(result.UpdatedAt)
	}
	pending := m.usageLoading && !m.usageRead[profile.ID]
	state := "Not checked"
	if pending {
		state = "Loading…"
	}
	if width < 100 {
		rows := []string{m.accountName(index, profile, width)}
		if result.Email != "" {
			rows = append(rows, muted.Render("  "+usageCell(result.Email, max(1, width-2))))
		}
		switch {
		case result.Error != "":
			rows = append(rows, lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render("  Status unavailable · More"))
		case !found:
			rows = append(rows, muted.Render("  "+state))
		default:
			for _, window := range result.Windows {
				line := usageGauge(window)
				if width >= 48 {
					line += "  ·  " + usageResetTime(window)
				}
				rows = append(rows, "  "+line)
			}
			if len(result.Windows) == 0 {
				rows = append(rows, muted.Render("  Quotas unavailable"))
			}
			resets := "Available resets: —"
			if result.ResetCountKnown {
				resets = fmt.Sprintf("Available resets: %d", result.ResetCount)
				if expiry := usageResetExpiry(result, time.Now()); expiry != "" {
					resets += " · " + expiry
				}
			}
			rows = append(rows, muted.Width(width).Render("  "+resets))
		}
		if pending && found {
			if metadata != "" {
				metadata += " · "
			}
			metadata += "Refreshing…"
		}
		if metadata != "" {
			rows = append(rows, muted.Render("  "+usageCell(metadata, max(1, width-2))))
		}
		return lipgloss.JoinVertical(lipgloss.Left, rows...)
	}

	accountWidth := width - 66
	account := lipgloss.JoinVertical(lipgloss.Left, m.accountName(index, profile, accountWidth),
		muted.Render(usageCell("  "+result.Email, accountWidth)),
		muted.Render(usageCell("  "+metadata, accountWidth)))
	var windows [2]string
	for i := range windows {
		windows[i] = muted.Render("—")
	}
	switch {
	case result.Error != "":
		windows[0] = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render("Status unavailable")
		windows[1] = muted.Render("See More")
	case !found:
		windows[0] = muted.Render(state)
	default:
		for i, window := range result.Windows {
			if i >= len(windows) {
				break
			}
			windows[i] = usageGauge(window) + "\n" + muted.Render(usageResetTime(window))
		}
	}
	if pending && found {
		windows[0] += "\n" + muted.Render("Refreshing…")
	}
	resets := "—"
	if found && result.Error == "" && result.ResetCountKnown {
		resets = fmt.Sprintf("%d", result.ResetCount)
		if expiry := usageResetExpiry(result, time.Now()); expiry != "" {
			resets += "\n" + muted.Render(strings.Replace(expiry, " ", "\n", 1))
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, usageCell(account, accountWidth), "  ",
		usageCell(windows[0], 25), "  ", usageCell(windows[1], 25), "  ", usageCell(resets, 10))
}

func (m *model) accountName(index int, profile switcher.Profile, width int) string {
	marker := "  "
	if index == m.cursor {
		marker = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Render("› ")
	}
	active := ""
	if profile.ID == m.activeID {
		active = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render(" (auth file)")
	}
	nameWidth := max(1, width-2-lipgloss.Width(active))
	name := lipgloss.NewStyle().MaxWidth(nameWidth).MaxHeight(1).Render(profile.Name)
	row := marker + name + active
	if index == m.cursor {
		row = lipgloss.NewStyle().Bold(true).Render(row)
	}
	return row
}

func usageCell(text string, width int) string {
	return lipgloss.NewStyle().Width(width).MaxWidth(width).Render(text)
}

func usageGauge(window switcher.UsageWindow) string {
	remaining := max(0, min(100, 100-window.Used))
	filled := (remaining*6 + 50) / 100
	color := "2"
	if remaining <= 10 {
		color = "1"
	} else if remaining <= 25 {
		color = "3"
	}
	bar := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(strings.Repeat("━", filled)) +
		lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(strings.Repeat("─", 6-filled))
	return fmt.Sprintf("%-4s %s %3d%%", usageWindowLabel(window.Duration), bar, remaining)
}

func usageResetTime(window switcher.UsageWindow) string {
	if !window.HasResetAt {
		return "Reset time unavailable"
	}
	return "Resets " + window.ResetsAt.Local().Format("02 Jan 15:04")
}

// usageResetExpiry uses the earliest reported expiry, independent of row order.
func usageResetExpiry(result switcher.AccountUsage, now time.Time) string {
	if !result.ResetCountKnown || result.ResetCount == 0 {
		return ""
	}
	var earliest time.Time
	for _, reset := range result.Resets {
		if reset.HasExpiry && !reset.ExpiresAt.IsZero() && (earliest.IsZero() || reset.ExpiresAt.Before(earliest)) {
			earliest = reset.ExpiresAt
		}
	}
	if earliest.IsZero() {
		return "Expiry unknown"
	}
	remaining := earliest.Sub(now)
	if remaining <= 0 {
		return "Expired"
	}
	var duration string
	switch {
	case remaining >= 24*time.Hour:
		duration = fmt.Sprintf("%dd %dh", int(remaining/(24*time.Hour)), int(remaining/time.Hour)%24)
	case remaining >= time.Hour:
		duration = fmt.Sprintf("%dh %dm", int(remaining/time.Hour), int(remaining/time.Minute)%60)
	case remaining >= time.Minute:
		duration = fmt.Sprintf("%dm", int(remaining/time.Minute))
	default:
		duration = "<1m"
	}
	return "Expires in " + duration
}

func (m *model) usageSummary(width int) string {
	checked, failed, resetAccounts, resets, loaded := 0, 0, 0, 0, 0
	for _, profile := range m.profiles {
		if m.usageRead[profile.ID] {
			loaded++
		}
		result, found := m.usageResult(profile.ID)
		if !found {
			continue
		}
		if result.Error != "" {
			failed++
			continue
		}
		checked++
		if result.ResetCountKnown {
			resetAccounts++
			resets += result.ResetCount
		}
	}
	summary := fmt.Sprintf("%d/%d accounts checked", checked, len(m.profiles))
	if failed > 0 {
		summary += fmt.Sprintf(" · %d unavailable", failed)
	}
	if resetAccounts > 0 {
		summary += fmt.Sprintf(" · %d available resets", resets)
		if resetAccounts < len(m.profiles) {
			summary += fmt.Sprintf(" (%d/%d accounts)", resetAccounts, len(m.profiles))
		}
	}
	if m.usageLoading {
		summary += fmt.Sprintf(" · Loading %d/%d", loaded, len(m.profiles))
	}
	return lipgloss.NewStyle().Width(width).Render(summary)
}
