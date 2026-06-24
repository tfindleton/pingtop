package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tfindleton/pingtop/internal/pingtop"
	"github.com/tfindleton/pingtop/internal/updates"
)

type AppConfig = pingtop.AppConfig
type EventEntry = pingtop.EventEntry
type PromptState = pingtop.PromptState
type StateSnapshot = pingtop.StateSnapshot
type TargetStats = pingtop.TargetStats
type UpdateStatus = updates.UpdateStatus

var (
	abbreviateCount      = pingtop.AbbreviateCount
	abbreviateRatio      = pingtop.AbbreviateRatio
	formatCompactSpan    = pingtop.FormatCompactSpan
	formatDuration       = pingtop.FormatDuration
	formatLatency        = pingtop.FormatLatency
	formatTimestampShort = pingtop.FormatTimestampShort
	nowLocalISO          = pingtop.NowLocalISO
	shorten              = pingtop.Shorten
	terminalSize         = TerminalSize
)

type textPair struct {
	plain    string
	rendered string
}

type screenChrome struct {
	width         int
	height        int
	visibleEvents []EventEntry
	headerLines   []string
	tableLines    []string
	footerBlock   []string
}

type eventPanelView struct {
	events         []EventEntry
	availableLines int
	start          int
	end            int
}

type Renderer struct {
	ansi                  bool
	lastRenderedLineCount int
}

func NewRenderer() *Renderer {
	return &Renderer{ansi: enableANSI(os.Stdout)}
}

func (renderer *Renderer) Enter() {
	if renderer.ansi {
		fmt.Fprint(os.Stdout, "\x1b[?1049h\x1b[2J\x1b[H\x1b[?25l")
	}
}

func (renderer *Renderer) Leave() {
	if renderer.ansi {
		fmt.Fprint(os.Stdout, "\x1b[0m\x1b[?25h\x1b[?1049l")
	}
}

func (renderer *Renderer) Draw(text string) {
	if !renderer.ansi {
		fmt.Fprint(os.Stdout, text)
		if !strings.HasSuffix(text, "\n") {
			fmt.Fprint(os.Stdout, "\n")
		}
		return
	}

	lines := strings.Split(text, "\n")
	for index, line := range lines {
		fmt.Fprintf(os.Stdout, "\x1b[%d;1H\x1b[2K%s", index+1, line)
	}
	for index := len(lines) + 1; index <= renderer.lastRenderedLineCount; index++ {
		fmt.Fprintf(os.Stdout, "\x1b[%d;1H\x1b[2K", index)
	}
	if _, height := terminalSize(); clearBelowAllowed(len(lines), height) {
		fmt.Fprintf(os.Stdout, "\x1b[%d;1H\x1b[J", len(lines)+1)
	}
	renderer.lastRenderedLineCount = len(lines)
}

func clearBelowAllowed(lineCount, height int) bool {
	if height <= 0 {
		return true
	}
	return lineCount < height
}

func (renderer *Renderer) BuildExitSummary(snapshot StateSnapshot) string {
	width := 72
	if terminalWidth, _ := terminalSize(); terminalWidth > 0 {
		width = minInt(maxInt(56, terminalWidth), 96)
	}
	diagnosisColor := renderer.diagnosisColor(snapshot.Diagnosis, false)
	title := renderer.style("pingtop", "green", true, false) + "  " + renderer.style("Session summary", "cyan", true, false)
	lines := []string{
		title,
		renderer.rule(width, "="),
		renderer.diagnosisBanner(snapshot.Diagnosis, width, diagnosisColor),
	}
	lines = append(lines,
		renderer.wrapPairs("Session", []textPair{
			renderer.kvPair("cycles", abbreviateCount(snapshot.Session.CyclesCompleted), "white", ""),
			renderer.kvPair("checks", abbreviateCount(snapshot.Session.TotalChecks), "white", ""),
			renderer.kvPair("ok", abbreviateCount(snapshot.Session.Successes), "white", "green"),
			renderer.kvPair("fail", abbreviateCount(snapshot.Session.Failures), "white", ternaryString(snapshot.Session.Failures > 0, "red", "green")),
			renderer.kvPair("dns", abbreviateCount(snapshot.Session.DNSFailures), "white", ternaryString(snapshot.Session.DNSFailures > 0, "yellow", "green")),
			renderer.kvPair("ping", abbreviateCount(snapshot.Session.PingFailures), "white", ternaryString(snapshot.Session.PingFailures > 0, "yellow", "green")),
		}, width, "cyan")...,
	)
	if !snapshot.LastCycleCompletedAt.IsZero() {
		durationSegments := []textPair{
			renderer.kvPair("completed", formatTimestampShort(snapshot.LastCycleCompletedAt), "white", ""),
		}
		if !snapshot.Session.StartedAt.IsZero() && !snapshot.LastCycleCompletedAt.Before(snapshot.Session.StartedAt) {
			durationSegments = append(durationSegments, renderer.kvPair(
				"ran",
				formatCompactSpan(maxInt(0, int(snapshot.LastCycleCompletedAt.Sub(snapshot.Session.StartedAt).Seconds()+0.5))),
				"white",
				"",
			))
		}
		lines = append(lines,
			renderer.wrapPairs("Last", durationSegments, width, "cyan")...,
		)
	}
	return strings.Join(lines, "\n")
}

func (renderer *Renderer) BuildScreen(
	snapshot StateSnapshot,
	config AppConfig,
	paused bool,
	helpVisible bool,
	prompt *PromptState,
	updateStatus UpdateStatus,
	eventScrollOffset int,
) string {
	chrome, eventView := renderer.buildScreenLayout(snapshot, config, paused, helpVisible, updateStatus, eventScrollOffset)
	topCapacity := maxInt(0, chrome.height-len(chrome.footerBlock))
	middleCapacity := chrome.middleCapacity()
	middleLines := make([]string, 0, middleCapacity)
	eventTitle := renderer.sectionTitle("Events", eventView.summary(), chrome.width)
	if middleCapacity <= len(chrome.tableLines) {
		middleLines = append(middleLines, chrome.tableLines[:middleCapacity]...)
	} else {
		middleLines = append(middleLines, chrome.tableLines...)
		remaining := middleCapacity - len(chrome.tableLines)
		if remaining >= 3 && eventView.availableLines > 0 {
			eventLines := renderer.buildEventPanel(eventView.events, chrome.width, eventView.start, eventView.end, true)
			eventBlock := []string{renderer.rule(chrome.width, "-"), eventTitle}
			eventBlock = append(eventBlock, eventLines...)
			if len(eventBlock) > remaining {
				eventBlock = eventBlock[:remaining]
			}
			middleLines = append(middleLines, eventBlock...)
		}
		for len(middleLines) < middleCapacity {
			middleLines = append(middleLines, "")
		}
	}

	topLines := append(append([]string(nil), chrome.headerLines...), middleLines...)
	if len(topLines) > topCapacity {
		topLines = topLines[:topCapacity]
	}
	for len(topLines) < topCapacity {
		topLines = append(topLines, "")
	}

	lines := append(topLines, chrome.footerBlock...)
	if prompt != nil {
		lines = renderer.overlayPrompt(lines, chrome.width, chrome.height, *prompt)
	}
	if len(lines) > chrome.height {
		lines = lines[:chrome.height]
	}
	return strings.Join(lines, "\n")
}

func (renderer *Renderer) EventScrollState(
	snapshot StateSnapshot,
	config AppConfig,
	paused bool,
	helpVisible bool,
	prompt *PromptState,
	updateStatus UpdateStatus,
	eventScrollOffset int,
) (int, int) {
	_, view := renderer.buildScreenLayout(snapshot, config, paused, helpVisible, updateStatus, eventScrollOffset)
	pageSize := view.availableLines
	if pageSize < 1 {
		pageSize = maxInt(3, config.VisibleEventLines)
	}
	return minInt(maxInt(eventScrollOffset, 0), view.maxScrollOffset()), pageSize
}

func (renderer *Renderer) BuildReport(snapshot StateSnapshot, config AppConfig, paused bool) string {
	width := 180
	header := []string{
		"pingtop session snapshot - " + nowLocalISO(timeNow(), false),
		"status: " + ternaryString(paused, "paused", "running"),
		"diagnosis: " + snapshot.Diagnosis,
		fmt.Sprintf(
			"check_interval_seconds=%.2f, ping_timeout_ms=%d, stats_window_seconds=%d, ui_refresh_interval_seconds=%.2f, diagnosis_confirm_cycles=%d, recovery_confirm_cycles=%d, latency_warning_ms=%d, latency_critical_ms=%d, log_rotation_max_mb=%d, log_rotation_keep_files=%d, logging_mode=%s, around_failure=%d/%ds, visible_event_lines=%d",
			config.CheckIntervalSeconds,
			config.PingTimeoutMS,
			config.StatsWindowSeconds,
			config.UIRefreshIntervalSeconds,
			config.DiagnosisConfirmCycles,
			config.RecoveryConfirmCycles,
			config.LatencyWarningMS,
			config.LatencyCriticalMS,
			config.LogRotationMaxMB,
			config.LogRotationKeepFiles,
			config.LoggingMode,
			config.AroundFailureBefore,
			config.AroundFailureAfter,
			config.VisibleEventLines,
		),
		fmt.Sprintf(
			"rolling_window: checks=%d, ok=%d, fail=%d, dns=%d, ping=%d",
			snapshot.SessionWindow.Checks,
			snapshot.SessionWindow.Successes,
			snapshot.SessionWindow.Failures,
			snapshot.SessionWindow.DNSFailures,
			snapshot.SessionWindow.PingFailures,
		),
		"",
	}
	visibleEvents := renderer.visibleEvents(snapshot.RecentEvents, config)
	body := renderer.buildTargetTable(snapshot.TargetStats, width, config, false)
	events := []string{"", "Recent events"}
	shownLines := minInt(15, config.VisibleEventLines)
	start := maxInt(0, len(visibleEvents)-shownLines)
	events = append(events, renderer.buildEventPanel(visibleEvents, width, start, len(visibleEvents), false)...)
	return strings.Join(append(append(header, body...), events...), "\n") + "\n"
}

func (renderer *Renderer) buildScreenChrome(
	snapshot StateSnapshot,
	config AppConfig,
	paused bool,
	helpVisible bool,
	updateStatus UpdateStatus,
	eventStatus string,
) screenChrome {
	width, height := terminalSize()
	if width < 40 {
		width = 40
	}
	if height < 20 {
		height = 20
	}

	status := "RUNNING"
	if paused {
		status = "PAUSED"
	}
	windowLabel := formatCompactSpan(snapshot.StatsWindowSeconds)
	rotationLabel := "off"
	if config.LogRotationMaxMB > 0 {
		rotationLabel = fmt.Sprintf("%dMB/%d", config.LogRotationMaxMB, config.LogRotationKeepFiles)
	}

	visibleEvents := renderer.visibleEvents(snapshot.RecentEvents, config)
	statusColor := renderer.diagnosisColor(snapshot.Diagnosis, paused)
	title := "pingtop " + pingtop.Version
	if eventStatus == "" {
		eventStatus = fmt.Sprintf("%d total", len(visibleEvents))
	}

	headerLines := []string{renderer.style(title, "green", true, false)}
	headerLines = append(headerLines, renderer.diagnosisBanner(snapshot.Diagnosis, width, statusColor))
	headerLines = append(headerLines,
		renderer.wrapPairs("Status", []textPair{
			renderer.kvPair("mode", status, "white", statusColor),
			renderer.kvPair("active", renderer.activeCycleText(snapshot.ActiveCycle), "white", renderer.activeCycleColor(snapshot.ActiveCycle)),
			renderer.kvPair("update", renderer.updateStatusText(updateStatus), "white", renderer.updateStatusColor(updateStatus)),
			renderer.kvPair("events", eventStatus, "white", ""),
			renderer.kvPair("last", formatTimestampShort(snapshot.LastCycleCompletedAt), "white", ""),
		}, width, statusColor)...,
	)
	headerLines = append(headerLines,
		renderer.wrapPairs("Timing", []textPair{
			renderer.kvPair("check", formatDuration(config.CheckIntervalSeconds), "white", ""),
			renderer.kvPair("timeout", fmt.Sprintf("%dms", config.PingTimeoutMS), "white", ""),
			renderer.kvPair("refresh", formatDuration(config.UIRefreshIntervalSeconds), "white", ""),
			renderer.kvPair("targets", fmt.Sprintf("%d", len(config.Targets)), "white", ""),
		}, width, "cyan")...,
	)
	headerLines = append(headerLines,
		renderer.wrapPairs("Config", []textPair{
			renderer.kvPair("stats", windowLabel, "white", ""),
			renderer.kvPair("confirm", fmt.Sprintf("%d/%d", config.DiagnosisConfirmCycles, config.RecoveryConfirmCycles), "white", ""),
			renderer.kvPair("latency", fmt.Sprintf("%d/%dms", config.LatencyWarningMS, config.LatencyCriticalMS), "white", ""),
			renderer.kvPair("logging", config.LoggingMode, "white", ""),
			renderer.kvPair("rotate", rotationLabel, "white", ""),
		}, width, "cyan")...,
	)
	headerLines = append(headerLines,
		renderer.wrapPairs("Rolling", []textPair{
			renderer.kvPair("checks", abbreviateCount(snapshot.SessionWindow.Checks), "white", ""),
			renderer.kvPair("ok", abbreviateCount(snapshot.SessionWindow.Successes), "white", "green"),
			renderer.kvPair("fail", abbreviateCount(snapshot.SessionWindow.Failures), "white", "red"),
			renderer.kvPair("dns", abbreviateCount(snapshot.SessionWindow.DNSFailures), "white", "yellow"),
			renderer.kvPair("ping", abbreviateCount(snapshot.SessionWindow.PingFailures), "white", "yellow"),
		}, width, "cyan")...,
	)
	headerLines = append(headerLines,
		renderer.wrapPairs("Session", []textPair{
			renderer.kvPair("cycles", abbreviateCount(snapshot.Session.CyclesCompleted), "white", ""),
			renderer.kvPair("checks", abbreviateCount(snapshot.Session.TotalChecks), "white", ""),
			renderer.kvPair("ok", abbreviateCount(snapshot.Session.Successes), "white", "green"),
			renderer.kvPair("fail", abbreviateCount(snapshot.Session.Failures), "white", "red"),
			renderer.kvPair("dns", abbreviateCount(snapshot.Session.DNSFailures), "white", "yellow"),
			renderer.kvPair("ping", abbreviateCount(snapshot.Session.PingFailures), "white", "yellow"),
		}, width, "cyan")...,
	)
	headerLines = append(headerLines, renderer.rule(width, "="))

	tableLines := renderer.buildTargetTable(snapshot.TargetStats, width, config, true)

	footerLines := make([]string, 0)
	if helpVisible {
		footerLines = append(footerLines,
			renderer.wrapPairs("Controls", []textPair{
				renderer.shortcutPair("q/Esc", "quit"),
				renderer.shortcutPair("p", "pause"),
				renderer.shortcutPair("h", "hide help"),
				renderer.shortcutPair("f", "fresh check"),
				renderer.shortcutPair("s", "snapshot"),
				renderer.shortcutPair("r", "reset"),
				renderer.shortcutPair("u", "updates"),
			}, width, "cyan")...,
		)
		footerLines = append(footerLines,
			renderer.wrapPairs("Tuning", []textPair{
				renderer.shortcutPair("l", "logging"),
				renderer.shortcutPair("+/-", "check"),
				renderer.shortcutPair("</>", "refresh -/+"),
				renderer.shortcutPair("w", "fail window"),
				renderer.shortcutPair("t", "stats window"),
			}, width, "cyan")...,
		)
		footerLines = append(footerLines,
			renderer.wrapPairs("Targets", []textPair{
				renderer.shortcutPair("a", "add"),
				renderer.shortcutPair("d", "delete"),
			}, width, "cyan")...,
		)
		footerLines = append(footerLines,
			renderer.wrapPairs("Events", []textPair{
				renderer.shortcutPair("Up/Down", "scroll"),
				renderer.shortcutPair("PgUp/PgDn", "page"),
			}, width, "cyan")...,
		)
	} else {
		footerLines = append(footerLines,
			renderer.wrapPairs("Help", []textPair{
				renderer.shortcutPair("h", "show help"),
				renderer.shortcutPair("q/Esc", "quit"),
			}, width, "cyan")...,
		)
	}

	footerBlock := []string{renderer.rule(width, "-")}
	footerBlock = append(footerBlock, footerLines...)

	return screenChrome{
		width:         width,
		height:        height,
		visibleEvents: visibleEvents,
		headerLines:   headerLines,
		tableLines:    tableLines,
		footerBlock:   footerBlock,
	}
}

func (renderer *Renderer) buildScreenLayout(
	snapshot StateSnapshot,
	config AppConfig,
	paused bool,
	helpVisible bool,
	updateStatus UpdateStatus,
	eventScrollOffset int,
) (screenChrome, eventPanelView) {
	chrome := renderer.buildScreenChrome(snapshot, config, paused, helpVisible, updateStatus, "")
	view := renderer.buildEventPanelView(chrome, config, eventScrollOffset)
	eventStatus := fmt.Sprintf("%d/%d", view.shownCount(), len(chrome.visibleEvents))
	chrome = renderer.buildScreenChrome(snapshot, config, paused, helpVisible, updateStatus, eventStatus)
	view = renderer.buildEventPanelView(chrome, config, eventScrollOffset)
	finalStatus := fmt.Sprintf("%d/%d", view.shownCount(), len(chrome.visibleEvents))
	if finalStatus != eventStatus {
		chrome = renderer.buildScreenChrome(snapshot, config, paused, helpVisible, updateStatus, finalStatus)
		view = renderer.buildEventPanelView(chrome, config, eventScrollOffset)
	}
	return chrome, view
}

func (chrome screenChrome) middleCapacity() int {
	topCapacity := maxInt(0, chrome.height-len(chrome.footerBlock))
	return maxInt(0, topCapacity-len(chrome.headerLines))
}

func (renderer *Renderer) buildEventPanelView(chrome screenChrome, config AppConfig, eventScrollOffset int) eventPanelView {
	view := eventPanelView{events: chrome.visibleEvents}
	if chrome.middleCapacity() <= len(chrome.tableLines) {
		return view
	}

	remaining := chrome.middleCapacity() - len(chrome.tableLines)
	if remaining < 5 {
		return view
	}

	view.availableLines = remaining - 2
	if view.availableLines < 3 {
		return view
	}

	clampedOffset := minInt(maxInt(eventScrollOffset, 0), view.maxScrollOffset())
	view.end = len(view.events) - clampedOffset
	view.start = maxInt(0, view.end-view.availableLines)
	return view
}

func (view eventPanelView) maxScrollOffset() int {
	if view.availableLines <= 0 {
		return 0
	}
	return maxInt(0, len(view.events)-view.availableLines)
}

func (view eventPanelView) shownCount() int {
	return maxInt(0, view.end-view.start)
}

func (view eventPanelView) summary() string {
	total := len(view.events)
	if total == 0 || view.shownCount() == 0 {
		return fmt.Sprintf("showing 0/%d", total)
	}
	return fmt.Sprintf("showing %d-%d/%d", view.start+1, view.end, total)
}

func (renderer *Renderer) style(text, fg string, bold, dim bool) string {
	if !renderer.ansi {
		return text
	}
	codes := make([]string, 0, 3)
	if bold {
		codes = append(codes, "1")
	}
	if dim {
		codes = append(codes, "2")
	}
	if code := colorCode(fg); code != "" {
		codes = append(codes, code)
	}
	if len(codes) == 0 {
		return text
	}
	return "\x1b[" + strings.Join(codes, ";") + "m" + text + "\x1b[0m"
}

func (renderer *Renderer) rule(width int, char string) string {
	return renderer.style(strings.Repeat(char, width), "blue", false, true)
}

func (renderer *Renderer) diagnosisBanner(diagnosis string, width int, color string) string {
	label := renderer.style("Diagnosis", color, true, false)
	return label + "  " + renderer.style(shorten(diagnosis, maxInt(1, width-11)), color, true, false)
}

func (renderer *Renderer) diagnosisColor(diagnosis string, paused bool) string {
	diagnosisLower := strings.ToLower(diagnosis)
	switch {
	case paused || strings.Contains(diagnosisLower, "waiting") || strings.Contains(diagnosisLower, "no targets"):
		return "yellow"
	case strings.Contains(diagnosisLower, "suspected") || strings.Contains(diagnosisLower, "confirming") || strings.Contains(diagnosisLower, "recovery observed"):
		return "yellow"
	case strings.Contains(diagnosisLower, "reachable"):
		return "green"
	default:
		return "red"
	}
}

func (renderer *Renderer) sectionTitle(title, subtitle string, width int) string {
	renderedTitle := renderer.style(title, "cyan", true, false)
	if subtitle == "" {
		return renderedTitle
	}
	return strings.TrimSpace(renderedTitle + "  " + shorten(subtitle, maxInt(1, width-len(title)-2)))
}

func (renderer *Renderer) kvPair(key, value, keyColor, valueColor string) textPair {
	plain := key + " " + value
	rendered := renderer.style(key, keyColor, false, true) + " " + renderer.style(value, valueColor, valueColor != "", false)
	return textPair{plain: plain, rendered: rendered}
}

func (renderer *Renderer) shortcutPair(key, description string) textPair {
	return textPair{
		plain:    "[" + key + "] " + description,
		rendered: renderer.style("["+key+"]", "yellow", true, false) + " " + description,
	}
}

func (renderer *Renderer) wrapPairs(label string, segments []textPair, width int, labelColor string) []string {
	prefixPlain := fmt.Sprintf("%-8s ", label)
	prefixRendered := renderer.style(fmt.Sprintf("%-8s", label), labelColor, true, false) + " "
	continuation := strings.Repeat(" ", len(prefixPlain))
	available := maxInt(8, width-len(prefixPlain))

	rows := make([][]textPair, 0, 2)
	current := make([]textPair, 0, len(segments))
	currentLength := 0
	for _, segment := range segments {
		if segment.plain == "" {
			continue
		}
		segmentLength := len(segment.plain)
		separatorLength := 0
		if len(current) > 0 {
			separatorLength = 2
		}
		if len(current) > 0 && currentLength+separatorLength+segmentLength > available {
			rows = append(rows, current)
			current = []textPair{segment}
			currentLength = segmentLength
			continue
		}
		current = append(current, segment)
		currentLength += separatorLength + segmentLength
	}
	if len(current) > 0 || len(rows) == 0 {
		rows = append(rows, current)
	}

	wrapped := make([]string, 0, len(rows))
	for index, row := range rows {
		prefix := continuation
		if index == 0 {
			prefix = prefixRendered
		}
		renderedParts := make([]string, 0, len(row))
		for _, segment := range row {
			renderedParts = append(renderedParts, segment.rendered)
		}
		content := "-"
		if len(renderedParts) > 0 {
			content = strings.Join(renderedParts, "  ")
		}
		wrapped = append(wrapped, prefix+content)
	}
	return wrapped
}

func (renderer *Renderer) overlayPrompt(lines []string, width, height int, prompt PromptState) []string {
	padded := append([]string(nil), lines...)
	for len(padded) < height {
		padded = append(padded, "")
	}
	boxWidth := minInt(maxInt(46, width-10), 88)
	boxWidth = minInt(boxWidth, maxInt(28, width-2))
	innerWidth := maxInt(18, boxWidth-4)
	boxHeight := 7
	if height < boxHeight+2 {
		return padded
	}

	title := renderer.promptTitle(prompt.Kind)
	message := shorten(prompt.Message, innerWidth)
	visibleInput := renderer.tailShorten(prompt.Buffer, maxInt(1, innerWidth-2))
	inputLine := "> "
	if visibleInput == "" {
		inputLine += "_"
	} else {
		inputLine += visibleInput
	}
	hint := shorten("Enter submit | Esc cancel | Backspace edit", innerWidth)

	boxLines := []string{
		"+" + strings.Repeat("-", boxWidth-2) + "+",
		fmt.Sprintf("| %-*s |", innerWidth, shorten(title, innerWidth)),
		fmt.Sprintf("| %-*s |", innerWidth, message),
		"|" + strings.Repeat(" ", boxWidth-2) + "|",
		fmt.Sprintf("| %-*s |", innerWidth, inputLine),
		fmt.Sprintf("| %-*s |", innerWidth, hint),
		"+" + strings.Repeat("-", boxWidth-2) + "+",
	}
	styledLines := []string{
		renderer.style(boxLines[0], "magenta", true, false),
		renderer.style(boxLines[1], "magenta", true, false),
		renderer.style(boxLines[2], "white", false, false),
		renderer.style(boxLines[3], "magenta", false, false),
		renderer.style(boxLines[4], "yellow", true, false),
		renderer.style(boxLines[5], "cyan", false, false),
		renderer.style(boxLines[6], "magenta", true, false),
	}

	top := maxInt(1, (height-boxHeight)/2)
	left := maxInt(0, (width-boxWidth)/2)
	rightPadding := maxInt(0, width-left-boxWidth)
	for index, line := range styledLines {
		targetIndex := top + index
		if targetIndex >= len(padded) {
			break
		}
		padded[targetIndex] = strings.Repeat(" ", left) + line + strings.Repeat(" ", rightPadding)
	}
	return padded
}

func (renderer *Renderer) promptTitle(kind string) string {
	switch kind {
	case "add":
		return "Add Target"
	case "delete":
		return "Delete Target"
	case "window":
		return "Around-Failure Window"
	case "stats_window":
		return "Stats Window"
	default:
		return "Prompt"
	}
}

func (renderer *Renderer) tailShorten(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if len(text) <= width {
		return text
	}
	if width <= 3 {
		return text[len(text)-width:]
	}
	return "..." + text[len(text)-(width-3):]
}

func (renderer *Renderer) buildTargetTable(statsList []TargetStats, width int, config AppConfig, ansi bool) []string {
	previousANSI := renderer.ansi
	renderer.ansi = ansi
	defer func() {
		renderer.ansi = previousANSI
	}()

	windowLabel := formatCompactSpan(config.StatsWindowSeconds)
	lossHeader := windowLabel + " Loss%"
	ratioHeader := windowLabel + " OK/Fail"
	lossWidth := maxInt(8, len(lossHeader))
	ratioWidth := maxInt(13, len(ratioHeader))

	lines := []string{renderer.style("Targets", "cyan", true, false)}
	header := fmt.Sprintf(
		"%3s %-24s %-8s %-10s %9s %6s %7s %*s %*s %-18s  %s",
		"Idx",
		"Target",
		"Type",
		"State",
		"Latency",
		"Consec",
		"Age",
		lossWidth,
		lossHeader,
		ratioWidth,
		ratioHeader,
		"Last IP",
		"Error",
	)
	lines = append(lines, renderer.style(shorten(header, width), "white", false, true))
	if len(statsList) == 0 {
		lines = append(lines, "  - no targets configured")
		return lines
	}

	for index, stats := range statsList {
		stateLabel := renderer.targetStateLabel(stats, config)
		statePlain := fmt.Sprintf("%-10s", stateLabel)
		stateText := renderer.style(statePlain, renderer.stateColor(stats, config), stats.LastState == "down" && !stats.Checking, false)
		latencyColor := renderer.latencyColor(stats.LastLatencyMS, config)
		latencyText := renderer.style(fmt.Sprintf("%9s", formatLatency(stats.LastLatencyMS)), latencyColor, latencyColor == "red", false)
		consecutiveText := renderer.style(
			fmt.Sprintf("%6d", stats.ConsecutiveFailures),
			ternaryString(stats.ConsecutiveFailures > 0, "red", "green"),
			stats.ConsecutiveFailures > 0,
			false,
		)
		lossText := renderer.style(
			fmt.Sprintf("%*.1f%%", lossWidth-1, stats.WindowSummary.LossPercentage()),
			renderer.lossColor(stats.WindowSummary.LossPercentage()),
			false,
			false,
		)
		okFailText := renderer.style(
			fmt.Sprintf("%*s", ratioWidth, abbreviateRatio(stats.WindowSummary.Successes, stats.WindowSummary.Failures)),
			ternaryString(stats.WindowSummary.Failures > 0, "red", "green"),
			false,
			false,
		)
		ageText := renderer.style(fmt.Sprintf("%7s", renderer.targetAge(stats)), renderer.targetAgeColor(stats, config), false, false)
		errorText := "-"
		if stats.LastErrorCategory != "" && stats.LastErrorCategory != "ok" {
			errorText = stats.LastErrorCategory
			if stats.LastErrorMessage != "" {
				errorText = stats.LastErrorCategory + ": " + stats.LastErrorMessage
			}
		}
		fixedWidth := 3 + 1 + 24 + 1 + 8 + 1 + 10 + 1 + 9 + 1 + 6 + 1 + 7 + 1 + lossWidth + 1 + ratioWidth + 1 + 18 + 2
		errorText = shorten(errorText, maxInt(10, width-fixedWidth))
		if errorText != "-" {
			errorText = renderer.style(errorText, "red", true, false)
		}

		line := fmt.Sprintf(
			"%3d %-24s %-8s %s %s %s %s %s %s %-18s  %s",
			index+1,
			shorten(stats.Target, 24),
			stats.TargetType,
			stateText,
			latencyText,
			consecutiveText,
			ageText,
			lossText,
			okFailText,
			shorten(defaultString(stats.LastResolvedIP, "-"), 18),
			errorText,
		)
		lines = append(lines, line)
	}
	return lines
}

func (renderer *Renderer) buildEventPanel(events []EventEntry, width, start, end int, ansi bool) []string {
	previousANSI := renderer.ansi
	renderer.ansi = ansi
	defer func() {
		renderer.ansi = previousANSI
	}()

	if len(events) == 0 {
		return []string{renderer.style("  - no notable failures, recoveries, or config changes yet", "white", false, true)}
	}
	start = minInt(maxInt(start, 0), len(events))
	end = minInt(maxInt(end, start), len(events))
	selected := events[start:end]
	if len(selected) == 0 {
		return nil
	}
	lines := make([]string, 0, len(selected))
	for _, event := range selected {
		prefix := fmt.Sprintf("%s %-5s", formatTimestampShort(event.Timestamp), strings.ToUpper(event.Level))
		lines = append(lines, renderer.style(shorten(prefix+" "+event.Message, width), renderer.eventColor(event.Level), event.Level == "warn" || event.Level == "error", false))
	}
	return lines
}

func (renderer *Renderer) interestingEvents(events []EventEntry) []EventEntry {
	selected := make([]EventEntry, 0, len(events))
	for _, event := range events {
		if renderer.isInterestingEvent(event) {
			selected = append(selected, event)
		}
	}
	return selected
}

func (renderer *Renderer) visibleEvents(events []EventEntry, config AppConfig) []EventEntry {
	if config.LoggingMode == pingtop.LoggingModeOff {
		return nil
	}
	return renderer.interestingEvents(events)
}

func (renderer *Renderer) isInterestingEvent(event EventEntry) bool {
	if event.Level == "warn" || event.Level == "error" {
		return true
	}
	notablePrefixes := []string{
		"Diagnosis changed:",
		"Monitoring ",
		"Counters reset",
		"Snapshot saved to ",
		"Added target ",
		"Deleted target ",
		"Logging mode set to ",
		"Check interval set to ",
		"UI refresh interval set to ",
		"Around-failure window set to ",
		"Stats window ",
	}
	for _, prefix := range notablePrefixes {
		if strings.HasPrefix(event.Message, prefix) {
			return true
		}
	}
	return strings.Contains(event.Message, " recovered (")
}

func (renderer *Renderer) activeCycleText(status pingtop.CycleStatus) string {
	if !status.Active {
		return "idle"
	}
	return fmt.Sprintf("cycle %d %d/%d", status.CycleID, status.CompletedChecks, status.TotalChecks)
}

func (renderer *Renderer) activeCycleColor(status pingtop.CycleStatus) string {
	if status.Active {
		return "yellow"
	}
	return "green"
}

func (renderer *Renderer) targetStateLabel(stats TargetStats, config AppConfig) string {
	if stats.Checking {
		return "checking"
	}
	if renderer.targetIsStale(stats, config) {
		return "stale"
	}
	return strings.ToLower(defaultString(stats.LastResult, "pending"))
}

func (renderer *Renderer) stateColor(stats TargetStats, config AppConfig) string {
	if stats.Checking || renderer.targetIsStale(stats, config) {
		return "yellow"
	}
	if stats.LastState == "up" {
		return "green"
	}
	if stats.LastState == "down" {
		return "red"
	}
	return "yellow"
}

func (renderer *Renderer) targetAge(stats TargetStats) string {
	if stats.LastCheckedAt.IsZero() {
		return "-"
	}
	ageSeconds := timeNow().Sub(stats.LastCheckedAt).Seconds()
	if ageSeconds < 0 {
		ageSeconds = 0
	}
	return formatDuration(ageSeconds)
}

func (renderer *Renderer) targetAgeColor(stats TargetStats, config AppConfig) string {
	if stats.Checking || renderer.targetIsStale(stats, config) {
		return "yellow"
	}
	return ""
}

func (renderer *Renderer) targetIsStale(stats TargetStats, config AppConfig) bool {
	if stats.Checking || stats.LastCheckedAt.IsZero() {
		return false
	}
	staleAfter := config.CheckIntervalSeconds + float64(config.PingTimeoutMS)/1000.0 + maxFloat(1.0, config.UIRefreshIntervalSeconds*2.0)
	return timeNow().Sub(stats.LastCheckedAt).Seconds() > staleAfter
}

func (renderer *Renderer) latencyColor(latencyMS *float64, config AppConfig) string {
	if latencyMS == nil {
		return ""
	}
	if *latencyMS >= float64(config.LatencyCriticalMS) {
		return "red"
	}
	if *latencyMS >= float64(config.LatencyWarningMS) {
		return "yellow"
	}
	return "green"
}

func (renderer *Renderer) lossColor(lossPercentage float64) string {
	if lossPercentage >= 50 {
		return "red"
	}
	if lossPercentage > 0 {
		return "yellow"
	}
	return "green"
}

func (renderer *Renderer) eventColor(level string) string {
	switch level {
	case "error":
		return "red"
	case "warn":
		return "yellow"
	default:
		return "cyan"
	}
}

func (renderer *Renderer) updateStatusText(status UpdateStatus) string {
	return status.Summary()
}

func (renderer *Renderer) updateStatusColor(status UpdateStatus) string {
	switch status.State {
	case "available":
		return "yellow"
	case "current":
		return "green"
	case "error":
		return "red"
	default:
		return ""
	}
}

func colorCode(name string) string {
	switch name {
	case "red":
		return "31"
	case "green":
		return "32"
	case "yellow":
		return "33"
	case "blue":
		return "34"
	case "magenta":
		return "35"
	case "cyan":
		return "36"
	case "white":
		return "37"
	default:
		return ""
	}
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func maxFloat(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func ternaryString(condition bool, whenTrue, whenFalse string) string {
	if condition {
		return whenTrue
	}
	return whenFalse
}

func timeNow() time.Time {
	return time.Now()
}
