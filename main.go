package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- STYLE WIZUALNE (LIP GLOSS) ---
var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(lipgloss.Color("#7D56F4")).
			Padding(0, 1).
			MarginBottom(1)

	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7D56F4")).
			Bold(true)

	viewportStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#555555")).
			Padding(0, 1)

	activeViewportStyle = viewportStyle.Copy().
				BorderForeground(lipgloss.Color("#7D56F4"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))
)

// --- TYPY WIADOMOŚCI (MESSAGES) ---
type logLineMsg string
type finishedMsg struct{ err error }

// --- MODEL APLIKACJI ---
type model struct {
	choices   []string
	cursor    int
	viewport  viewport.Model
	logLines  []string
	running   bool
	focusLogs bool // Czy strzałki obsługują teraz przewijanie logów
}

func initialModel() model {
	files, _ := os.ReadDir(".")
	var batFiles []string
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(strings.ToLower(f.Name()), ".bat") {
			batFiles = append(batFiles, f.Name())
		}
	}

	vp := viewport.New(80, 15)
	vp.SetContent("Wybierz skrypt i naciśnij ENTER, aby rozpocząć...")

	return model{
		choices:  batFiles,
		viewport: vp,
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit

		case "q", "esc":
			if m.focusLogs {
				m.focusLogs = false // Wyłączamy tryb przeglądania logów
				return m, nil
			}
			return m, tea.Quit // Jeśli nie jesteśmy w logach, zamykamy apkę

		case "up", "k":
			if m.focusLogs {
				// Jeśli fokus na logi, pozwalamy viewportowi obsłużyć ruch
				m.viewport, cmd = m.viewport.Update(msg)
				return m, cmd
			}
			if !m.running && m.cursor > 0 {
				m.cursor--
			}

		case "down", "j":
			if m.focusLogs {
				// Jeśli fokus na logi, pozwalamy viewportowi obsłużyć ruch
				m.viewport, cmd = m.viewport.Update(msg)
				return m, cmd
			}
			if !m.running && m.cursor < len(m.choices)-1 {
				m.cursor++
			}

		case "enter":
			if !m.running && len(m.choices) > 0 {
				m.running = true
				m.focusLogs = false // Na czas trwania skryptu nie mrozimy menu, ale logi same scrollują
				m.logLines = []string{"[SYSTEM] Uruchamianie: " + m.choices[m.cursor] + "..."}
				m.viewport.SetContent(strings.Join(m.logLines, "\n"))

				target := m.choices[m.cursor]
				return m, m.runBatScript(target)
			}
		}

	case logLineMsg:
		m.logLines = append(m.logLines, string(msg))
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
		m.viewport.GotoBottom()

	case finishedMsg:
		m.running = false
		m.focusLogs = true // Po skończeniu automatycznie dajemy fokus na logi do przejrzenia
		status := "SUKCES"
		if msg.err != nil {
			status = fmt.Sprintf("BŁĄD (%v)", msg.err)
		}
		m.logLines = append(m.logLines, fmt.Sprintf("\n[SYSTEM] Proces zakończony: %s", status))
		m.logLines = append(m.logLines, "[SYSTEM] Tryb przeglądania logów aktywny. Naciśnij 'q' aby wrócić do listy.")
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
		m.viewport.GotoBottom()
	}

	// Obsługa scrolla myszy zawsze działa dla viewportu
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m model) runBatScript(filename string) tea.Cmd {
	return func() tea.Msg {
		logFilename := strings.TrimSuffix(filename, ".bat") + ".log"
		logFile, err := os.Create(logFilename)
		if err != nil {
			return finishedMsg{err: fmt.Errorf("nie można utworzyć pliku logu: %w", err)}
		}
		defer logFile.Close()

		c := exec.Command("cmd", "/c", filename)
		stdout, _ := c.StdoutPipe()
		stderr, _ := c.StderrPipe()
		scriptOutput := io.MultiReader(stdout, stderr)

		if err := c.Start(); err != nil {
			return finishedMsg{err: err}
		}

		teeReader := io.TeeReader(scriptOutput, logFile)
		scanner := bufio.NewScanner(teeReader)
		for scanner.Scan() {
			line := scanner.Text()
			p.Send(logLineMsg(line))
		}

		err = c.Wait()
		return finishedMsg{err: err}
	}
}

func (m model) View() string {
	var s strings.Builder

	s.WriteString(headerStyle.Render(" BAT LAUNCHER GO ") + "\n")

	// Lista plików
	if len(m.choices) == 0 {
		s.WriteString("  Brak plików .bat w tym folderze.\n")
	} else {
		for i, choice := range m.choices {
			cursor := "  "
			line := choice
			if m.cursor == i {
				cursor = "> "
				if !m.focusLogs {
					line = selectedStyle.Render(choice)
				} else {
					line = lipgloss.NewStyle().Foreground(lipgloss.Color("#aaaaaa")).Render(choice)
				}
			}
			s.WriteString(fmt.Sprintf("%s%s\n", cursor, line))
		}
	}

	// Panel logów - zmienia kolor ramki gdy ma fokus
	vStyle := viewportStyle
	logHeader := " LOGI TERMINALA (AUTO-ZAPIS DO .LOG) "
	if m.focusLogs {
		vStyle = activeViewportStyle
		logHeader = " TRYB PRZEGLĄDANIA LOGÓW (NACIŚNIJ Q ABY WRÓCIĆ) "
	}

	s.WriteString("\n" + headerStyle.Copy().Background(lipgloss.Color("#333333")).Render(logHeader) + "\n")
	s.WriteString(vStyle.Render(m.viewport.View()) + "\n")

	help := " q: wyjdź • enter: uruchom • ↑/↓: nawigacja • myszka: scroll"
	if m.focusLogs {
		help = " q: powrót do listy plików • ↑/↓: przewijanie logów"
	}
	s.WriteString(helpStyle.Render(help))

	return s.String()
}

var p *tea.Program

func main() {
	m := initialModel()
	p = tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())

	if _, err := p.Run(); err != nil {
		fmt.Printf("Wystąpił krytyczny błąd: %v", err)
		os.Exit(1)
	}
}
