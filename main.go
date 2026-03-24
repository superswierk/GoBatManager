package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- ZMIENNE GLOBALNE ---
// Wersja aplikacji (może być nadpisana przez flagi kompilacji)
var version = "dev"

// Plik przechowujący czasy wykonania skryptów
const timesFile = "script_times.json"

// --- FUNKCJE POMOCNICZE BAZY DANYCH (JSON) ---
// loadTimes wczytuje mapę z czasami z pliku JSON.
func loadTimes() map[string]string {
	times := make(map[string]string)
	data, err := os.ReadFile(timesFile)
	if err == nil {
		json.Unmarshal(data, &times)
	}
	return times
}

// saveTimes zapisuje mapę czasów do pliku JSON w czytelnym formacie.
func saveTimes(times map[string]string) {
	data, err := json.MarshalIndent(times, "", "  ")
	if err == nil {
		os.WriteFile(timesFile, data, 0644)
	}
}

// --- STYLE WIZUALNE (LIP GLOSS) ---
var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(lipgloss.Color("#7D56F4")).
			Padding(0, 1)

	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7D56F4")).
			Bold(true)

	// Styl dla elementów w kolejce (z numerem)
	checkedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00CBCB")).
			Bold(true)

	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF00"))
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF0000"))

	viewportStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#555555")).
			Padding(0, 1)

	// Ramka zmieniająca kolor, gdy logi są aktywne (focus)
	activeViewportStyle = viewportStyle.Copy().
				BorderForeground(lipgloss.Color("#7D56F4"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))

	timerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFA500")).
			Bold(true)

	savedTimeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))

	previewViewportStyle = viewportStyle.Copy().
				BorderForeground(lipgloss.Color("#00BFFF"))
)

// --- TYPY WIADOMOŚCI ---
type logLineMsg string // Nowa linia tekstu wysłana do UI
type tickMsg time.Time  // Sygnał do odświeżenia stopera

// Wiadomość o zakończeniu procesu
type finishedMsg struct {
	err  error
	name string
}

// --- MODEL APLIKACJI ---
type model struct {
	choices      []string          // Lista dostępnych plików .bat / .sh
	cursor       int               // Pozycja kursora w menu
	selectedIdxs []int             // Lista indeksów wybranych do kolejki (zachowuje kolejność)
	results      map[string]bool   // Wyniki wykonania (true = sukces, false = błąd/przerwanie)
	activeQueue  []int             // Kopia kolejki na czas uruchomienia
	viewport     viewport.Model    // Komponent do scrollowania logów
	logLines     []string          // Bufor wszystkich linii tekstu
	running      bool              // Czy skrypt aktualnie pracuje
	currentCmd   *exec.Cmd         // Referencja do procesu (umożliwia przerwanie przez Ctrl+C)
	focusLogs    bool              // Czy sterowanie klawiaturą jest w oknie logów
	ready        bool              // Czy wymiary okna zostały zainicjowane
	extension    string            // .bat dla Windows, .sh dla reszty
	width        int               // Szerokość okna
	height       int               // Wysokość okna
	startTime    time.Time         // Czas startu aktualnego zadania
	elapsed      time.Duration     // Aktualny czas trwania (stoper)
	scriptTimes  map[string]string // Baza poprzednich czasów wykonania
	previewing   bool              // Czy wyświetlany jest podgląd kodu pliku
}

// Inicjalizacja modelu startowego
func initialModel() model {
	ext := ".sh"
	if runtime.GOOS == "windows" {
		ext = ".bat"
	}

	files, _ := os.ReadDir(".")
	var scripts []string
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(strings.ToLower(f.Name()), ext) {
			scripts = append(scripts, f.Name())
		}
	}

	return model{
		choices:      scripts,
		extension:    ext,
		selectedIdxs: []int{},
		results:      make(map[string]bool),
		scriptTimes:  loadTimes(),
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

// tick co sekundę odświeża interfejs (stoper)
func tick() tea.Cmd {
	return tea.Every(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Update - serce logiki aplikacji, reaguje na klawisze i zdarzenia systemowe
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	switch msg := msg.(type) {
	// Obsługa kółka myszy w oknie logów
	case tea.MouseMsg:
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)

	case tickMsg:
		if m.running {
			m.elapsed = time.Since(m.startTime)
			return m, tick()
		}

	// Dynamiczne skalowanie interfejsu przy zmianie rozmiaru okna terminala
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		leftWidth := msg.Width / 3
		if leftWidth < 28 {
			leftWidth = 28
		}
		vpWidth := msg.Width - leftWidth - 4
		vpHeight := msg.Height - 6

		if !m.ready {
			m.viewport = viewport.New(vpWidth, vpHeight)
			m.viewport.SetContent("Wybierz skrypt i naciśnij ENTER...")
			m.ready = true
		} else {
			m.viewport.Width = vpWidth
			m.viewport.Height = vpHeight
		}

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			// KLUCZOWA ZMIANA: Jeśli skrypt działa, przerywamy go zamiast zamykać apkę
			if m.running && m.currentCmd != nil && m.currentCmd.Process != nil {
				m.logLines = append(m.logLines, errorStyle.Render("\n[SYSTEM] PRZERWANO PRZEZ UŻYTKOWNIKA (Ctrl+C)..."))

				// Windows potrzebuje taskkill, by ubić też procesy potomne (np. to co wywołał .bat)
				if runtime.GOOS == "windows" {
					_ = exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprintf("%d", m.currentCmd.Process.Pid)).Run()
				} else {
					_ = m.currentCmd.Process.Kill()
				}
				// Nie wywołujemy tea.Quit, by launcher został otwarty
				return m, nil
			}
			return m, tea.Quit // Jeśli nic nie działa, Ctrl+C wychodzi z programu

		case "q", "esc":
			if m.previewing {
				m.previewing = false
				m.refreshViewportContent()
				return m, nil
			}
			if m.focusLogs {
				m.focusLogs = false
				return m, nil
			}
			return m, tea.Quit

		case " ":
			// Tryb podglądu pliku (Space)
			if !m.running && len(m.choices) > 0 {
				m.previewing = !m.previewing
				if m.previewing {
					m = m.updatePreview()
				} else {
					m.refreshViewportContent()
				}
			}
			return m, nil

		case "x":
			// Dynamiczne zarządzanie kolejnością wykonywania (Kolejka)
			if !m.running && len(m.choices) > 0 {
				found := -1
				for i, idx := range m.selectedIdxs {
					if idx == m.cursor {
						found = i
						break
					}
				}
				if found != -1 {
					// Usuwamy i przesuwamy resztę w górę
					m.selectedIdxs = append(m.selectedIdxs[:found], m.selectedIdxs[found+1:]...)
				} else {
					// Dodajemy na koniec kolejki
					m.selectedIdxs = append(m.selectedIdxs, m.cursor)
				}
			}

		case "up", "k":
			if m.focusLogs {
				m.viewport, cmd = m.viewport.Update(msg)
				return m, cmd
			}
			if !m.running && m.cursor > 0 {
				m.cursor--
				if m.previewing {
					m = m.updatePreview()
				}
			}

		case "down", "j":
			if m.focusLogs {
				m.viewport, cmd = m.viewport.Update(msg)
				return m, cmd
			}
			if !m.running && m.cursor < len(m.choices)-1 {
				m.cursor++
				if m.previewing {
					m = m.updatePreview()
				}
			}

		case "enter":
			// Start procesu lub całej kolejki
			if !m.running && len(m.choices) > 0 {
				m.running = true
				m.focusLogs = false
				m.previewing = false
				if len(m.selectedIdxs) > 0 {
					m.activeQueue = make([]int, len(m.selectedIdxs))
					copy(m.activeQueue, m.selectedIdxs)
				} else {
					m.activeQueue = []int{m.cursor}
				}
				return m.runNextInQueue()
			}
		}

	case logLineMsg:
		// Dodawanie linii logu ze skryptu do viewportu
		m.logLines = append(m.logLines, string(msg))
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
		m.viewport.GotoBottom()

	case finishedMsg:
		// Zakończenie pojedynczego zadania
		m.running = false
		m.currentCmd = nil
		m.elapsed = time.Since(m.startTime)

		timeFormatted := m.elapsed.Round(time.Second).String()
		m.scriptTimes[msg.name] = timeFormatted
		saveTimes(m.scriptTimes)

		status := "SUKCES"
		if msg.err != nil {
			status = fmt.Sprintf("ZAKOŃCZONO (%v)", msg.err)
			m.results[msg.name] = false
		} else {
			m.results[msg.name] = true
		}

		m.logLines = append(m.logLines, fmt.Sprintf("\n[SYSTEM] Proces zakończony: %s", status))
		m.logLines = append(m.logLines, fmt.Sprintf("[SYSTEM] Czas trwania: %s", timeFormatted))
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
		m.viewport.GotoBottom()

		// Idziemy dalej w kolejce
		if len(m.activeQueue) > 0 {
			m.running = true
			return m.runNextInQueue()
		}

		m.focusLogs = true
		m.logLines = append(m.logLines, "[SYSTEM] Zadania zakończone. Naciśnij 'q' aby wrócić.")
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
		m.selectedIdxs = []int{} // Czyścimy kolejkę po skończeniu
	}

	return m, tea.Batch(cmds...)
}

// runNextInQueue obsługuje logikę przechodzenia do kolejnego elementu listy zadań
func (m model) runNextInQueue() (model, tea.Cmd) {
	if len(m.activeQueue) == 0 {
		m.running = false
		return m, nil
	}

	idx := m.activeQueue[0]
	m.activeQueue = m.activeQueue[1:]
	m.cursor = idx

	m.startTime = time.Now()
	m.elapsed = 0
	msg := fmt.Sprintf("[SYSTEM] >>> Uruchamianie %s...", m.choices[idx])
	m.logLines = append(m.logLines, "\n"+msg)
	m.viewport.SetContent(strings.Join(m.logLines, "\n"))

	target := m.choices[idx]

	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.Command("cmd", "/c", target)
	} else {
		c = exec.Command("sh", target)
	}

	m.currentCmd = c // Zapisujemy, by Ctrl+C mógł do niego "dosięgnąć"

	// POPRAWKA: Zwracamy model wraz z funkcją runScript jako Cmd
	return m, m.runScript(c, target)
}

// updatePreview wczytuje treść pliku do okna podglądu
func (m model) updatePreview() model {
	if len(m.choices) == 0 {
		return m
	}
	filename := m.choices[m.cursor]
	content, err := os.ReadFile(filename)
	if err != nil {
		m.viewport.SetContent(fmt.Sprintf("Błąd odczytu pliku: %v", err))
		return m
	}
	m.viewport.SetContent(string(content))
	m.viewport.GotoTop()
	return m
}

// refreshViewportContent przywraca logi terminala po zamknięciu podglądu
func (m *model) refreshViewportContent() {
	if len(m.logLines) == 0 {
		m.viewport.SetContent("Wybierz skrypt i naciśnij ENTER...")
	} else {
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
		m.viewport.GotoBottom()
	}
}

// runScript - asynchroniczny streaming wyjścia konsoli do UI i do pliku .log
func (m model) runScript(c *exec.Cmd, filename string) tea.Cmd {
	return func() tea.Msg {
		logFilename := strings.TrimSuffix(filename, m.extension) + ".log"
		logFile, err := os.Create(logFilename)
		if err != nil {
			return finishedMsg{err: fmt.Errorf("błąd logu: %w", err), name: filename}
		}
		defer logFile.Close()

		stdout, _ := c.StdoutPipe()
		stderr, _ := c.StderrPipe()
		scriptOutput := io.MultiReader(stdout, stderr)

		if err := c.Start(); err != nil {
			return finishedMsg{err: fmt.Errorf("błąd startu: %w", err), name: filename}
		}

		// TeeReader: rozdziela strumień na plik i na interfejs użytkownika
		teeReader := io.TeeReader(scriptOutput, logFile)
		reader := bufio.NewReader(teeReader)
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				p.Send(logLineMsg(strings.TrimRight(line, "\r\n")))
			}
			if err != nil {
				break
			}
		}

		err = c.Wait()
		return finishedMsg{err: err, name: filename}
	}
}

// View - budowanie interfejsu graficznego (renderowanie klatek)
func (m model) View() string {
	if !m.ready {
		return "\n  Inicjalizacja systemu..."
	}

	// 1. Nagłówek
	headerText := fmt.Sprintf(" SCRIPT LAUNCHER (%s) v%s ", strings.ToUpper(runtime.GOOS), version)
	header := headerStyle.Render(headerText)

	leftWidth := m.width / 3
	if leftWidth < 28 {
		leftWidth = 28
	}

	// 2. Budowanie listy plików (Lewy Panel)
	var leftBuilder strings.Builder
	leftTitle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4")).Render("LISTA SKRYPTÓW:")
	leftBuilder.WriteString(leftTitle + "\n\n")

	if len(m.choices) == 0 {
		leftBuilder.WriteString("Brak pasujących plików w folderze.\n")
	} else {
		for i, choice := range m.choices {
			cursor := "  "
			qPos := -1
			for pos, idx := range m.selectedIdxs {
				if idx == i {
					qPos = pos + 1
					break
				}
			}

			// Ikona (N) dla kolejki lub puste nawiasy
			checked := " [ ] "
			if qPos != -1 {
				checked = fmt.Sprintf(" (%d) ", qPos)
			}

			// Ikona rezultatu (✔ / ✘)
			resultIcon := ""
			if res, ok := m.results[choice]; ok {
				if res {
					resultIcon = successStyle.Render("✔ ")
				} else {
					resultIcon = errorStyle.Render("✘ ")
				}
			}

			line := choice
			if m.cursor == i {
				cursor = "> "
				if !m.focusLogs {
					line = selectedStyle.Render(choice)
				} else {
					line = lipgloss.NewStyle().Foreground(lipgloss.Color("#aaaaaa")).Render(choice)
				}
			} else if qPos != -1 {
				line = checkedStyle.Render(choice)
			}

			// Czas ostatniego wykonania z bazy JSON
			timeBadge := ""
			if savedTime, exists := m.scriptTimes[choice]; exists {
				timeBadge = savedTimeStyle.Render(fmt.Sprintf(" (%s)", savedTime))
			}

			leftBuilder.WriteString(fmt.Sprintf("%s%s%s%s%s\n", cursor, resultIcon, checked, line, timeBadge))
		}
	}

	leftBox := lipgloss.NewStyle().Width(leftWidth).Height(m.height - 3).PaddingRight(2).Render(leftBuilder.String())

	// 3. Budowanie Logów (Prawy Panel)
	vStyle := viewportStyle
	logHeader := " LOGI TERMINALA (AUTO-ZAPIS DO PLIKU) "
	timeStr := ""
	if m.running || m.elapsed > 0 {
		timeStr = timerStyle.Render(fmt.Sprintf(" [%s] ", m.elapsed.Round(time.Second)))
	}

	if m.previewing {
		vStyle = previewViewportStyle
		logHeader = fmt.Sprintf(" PODGLĄD PLIKU: %s ", m.choices[m.cursor])
		timeStr = ""
	} else if m.focusLogs {
		vStyle = activeViewportStyle
		logHeader = " PRZEGLĄDANIE HISTORII LOGÓW (Q = POWRÓT) "
	}

	rightTitle := lipgloss.NewStyle().Background(lipgloss.Color("#333333")).Foreground(lipgloss.Color("#FFFFFF")).Padding(0, 1).Render(logHeader)
	logBar := lipgloss.JoinHorizontal(lipgloss.Center, rightTitle, timeStr)

	rightBox := lipgloss.JoinVertical(lipgloss.Left, logBar, vStyle.Render(m.viewport.View()))

	// Połączenie paneli w jedną linię
	mainContent := lipgloss.JoinHorizontal(lipgloss.Top, leftBox, rightBox)

	// 4. Dynamiczny pasek pomocy na dole
	help := " q: wyjdź • x: kolejka • enter: start • ctrl+c: przerwij skrypt • ↑/↓: nawigacja"
	if m.running {
		help = " ctrl+c: PRZERWIJ DZIAŁANIE • ↑/↓: przewijanie logów"
	} else if m.previewing {
		help = " spacja/q/esc: zamknij podgląd • myszka: przewijanie"
	} else if m.focusLogs {
		help = " q: powrót do listy plików • ↑/↓: przewijanie logów kółkiem"
	}
	footer := helpStyle.Render(help)

	return lipgloss.JoinVertical(lipgloss.Left, header, "", mainContent, footer)
}

// Główny punkt wejścia do aplikacji
var p *tea.Program

func main() {
	m := initialModel()
	// AltScreen tworzy osobny bufor terminala (nie niszczy widoku konsoli po wyjściu)
	p = tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Wystąpił błąd krytyczny: %v\n", err)
		os.Exit(1)
	}
}
