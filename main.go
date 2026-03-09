package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- STYLE WIZUALNE (LIP GLOSS) ---
// Definiujemy style raz, aby używać ich wielokrotnie w metodzie View()
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

	// Standardowa ramka dla okna logów
	viewportStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#555555")).
			Padding(0, 1)

	// Ramka zmieniająca kolor na fioletowy, gdy użytkownik przewija logi
	activeViewportStyle = viewportStyle.Copy().
				BorderForeground(lipgloss.Color("#7D56F4"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))
)

// --- TYPY WIADOMOŚCI (MESSAGES) ---
// W Bubble Tea komunikacja odbywa się za pomocą struktur/typów przesyłanych do funkcji Update
type logLineMsg string               // Przesyła linię tekstu ze skryptu do UI
type finishedMsg struct{ err error } // Informuje o zakończeniu procesu

// --- MODEL APLIKACJI ---
// Główna struktura przechowująca stan całej aplikacji
type model struct {
	choices   []string       // Lista znalezionych plików .bat
	cursor    int            // Indeks aktualnie podświetlonego pliku
	viewport  viewport.Model // Komponent Bubbles do obsługi przewijanego tekstu
	logLines  []string       // Bufor przechowujący wszystkie linie logów
	running   bool           // Czy skrypt jest w trakcie wykonywania
	focusLogs bool           // Czy sterowanie (strzałki) jest przekierowane na logi
	ready     bool           // Czy otrzymaliśmy WindowSizeMsg i zainicjowaliśmy wymiary
	extension string         // Wykryte rozszerzenie (.bat lub .sh)
}

// Funkcja inicjalizująca model startowy
func initialModel() model {

	ext := ".sh"
	if runtime.GOOS == "windows" {
		ext = ".bat"
	}

	files, _ := os.ReadDir(".")
	var scripts []string
	for _, f := range files {
		// Szukamy tylko plików z rozszerzeniem .bat
		if !f.IsDir() && strings.HasSuffix(strings.ToLower(f.Name()), ext) {
			scripts = append(scripts, f.Name())
		}
	}

	return model{
		choices:   scripts,
		extension: ext,
	}
}

// Init wywoływane na starcie aplikacji (można tu zwrócić Cmd na start)
func (m model) Init() tea.Cmd {
	return nil
}

// Update - serce aplikacji, reaguje na zdarzenia (klawisze, wiadomości systemowe)
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	switch msg := msg.(type) {
	case tea.MouseMsg:
		m.viewport, cmd = m.viewport.Update(msg)
	// tea.WindowSizeMsg przychodzi na starcie i przy każdej zmianie rozmiaru okna terminala
	case tea.WindowSizeMsg:
		headerHeight := 3
		listHeight := len(m.choices) + 2
		footerHeight := 3
		reservedHeight := headerHeight + listHeight + footerHeight

		// Inicjalizacja lub aktualizacja wymiarów viewportu
		if !m.ready {
			m.viewport = viewport.New(msg.Width-4, msg.Height-reservedHeight)
			m.viewport.SetContent("Wybierz skrypt i naciśnij ENTER...")
			m.ready = true
		} else {
			m.viewport.Width = msg.Width - 4
			m.viewport.Height = msg.Height - reservedHeight
		}

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit

		case "q", "esc":
			// Jeśli przeglądamy logi, 'q' wraca do menu. Jeśli nie - zamyka program.
			if m.focusLogs {
				m.focusLogs = false
				return m, nil
			}
			return m, tea.Quit

		case "up", "k":
			if m.focusLogs {
				// Przekazujemy klawisz do komponentu viewport, aby przewinął tekst
				m.viewport, cmd = m.viewport.Update(msg)
				return m, cmd
			}
			if !m.running && m.cursor > 0 {
				m.cursor--
			}

		case "down", "j":
			if m.focusLogs {
				m.viewport, cmd = m.viewport.Update(msg)
				return m, cmd
			}
			if !m.running && m.cursor < len(m.choices)-1 {
				m.cursor++
			}

		case "enter":
			// Blokada przed wielokrotnym uruchomieniem
			if !m.running && len(m.choices) > 0 {
				m.running = true
				m.focusLogs = false
				m.logLines = []string{"[SYSTEM] Uruchamianie: " + m.choices[m.cursor] + "..."}
				m.viewport.SetContent(strings.Join(m.logLines, "\n"))
				target := m.choices[m.cursor]
				return m, m.runScript(target)
			}
		}

	// Nowa linia tekstu przyszła z goroutine (procesu tła)
	case logLineMsg:
		m.logLines = append(m.logLines, string(msg))
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
		m.viewport.GotoBottom() // Automatyczne przewijanie przy nowych logach

	// Proces .bat zakończył się
	case finishedMsg:
		m.running = false
		m.focusLogs = true // Aktywujemy tryb przeglądania logów po zakończeniu
		status := "SUKCES"
		if msg.err != nil {
			status = fmt.Sprintf("BŁĄD (%v)", msg.err)
		}
		m.logLines = append(m.logLines, fmt.Sprintf("\n[SYSTEM] Proces zakończony: %s", status))
		m.logLines = append(m.logLines, "[SYSTEM] Tryb przeglądania logów aktywny. Naciśnij 'q' aby wrócić do listy.")
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
		m.viewport.GotoBottom()
	}

	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

// Funkcja pomocnicza do asynchronicznego uruchamiania skryptu
func (m model) runScript(filename string) tea.Cmd {
	return func() tea.Msg {
		logFilename := strings.TrimSuffix(filename, m.extension) + ".log"

		// 1. Przygotowanie pliku logu
		logFile, err := os.Create(logFilename)
		if err != nil {
			return finishedMsg{err: fmt.Errorf("błąd zapisu pliku .log: %w", err)}
		}
		defer logFile.Close()

		// 2. Dobór komendy w zależności od systemu operacyjnego
		var c *exec.Cmd
		if runtime.GOOS == "windows" {
			c = exec.Command("cmd", "/c", filename)
		} else {
			c = exec.Command("sh", filename)
		}

		// Pobieramy pipe dla wyjścia standardowego i błędów
		stdout, err := c.StdoutPipe()
		if err != nil {
			return finishedMsg{err: fmt.Errorf("błąd stdout: %w", err)}
		}

		stderr, err := c.StderrPipe()
		if err != nil {
			return finishedMsg{err: fmt.Errorf("błąd stderr: %w", err)}
		}

		// Łączymy oba strumienie, aby czytać błędy i sukcesy jednocześnie
		scriptOutput := io.MultiReader(stdout, stderr)

		// 3. Start procesu
		if err := c.Start(); err != nil {
			return finishedMsg{err: fmt.Errorf("nie udało się uruchomić skryptu: %w", err)}
		}

		// TeeReader: kopiuje wszystko co przeczyta ze skryptu bezpośrednio do pliku .log
		teeReader := io.TeeReader(scriptOutput, logFile)
		scanner := bufio.NewScanner(teeReader)
		for scanner.Scan() {
			// Pętla wysyła linie tekstu do głównej pętli programu Bubble Tea
			p.Send(logLineMsg(scanner.Text()))
		}

		if err := scanner.Err(); err != nil {
			p.Send(logLineMsg(fmt.Sprintf("[SYSTEM BŁĄD] Problem z odczytem wyjścia: %v", err)))
		}

		// 4. Czekamy na fizyczne zakończenie procesu
		err = c.Wait()
		return finishedMsg{err: err}
	}
}

// View odpowiada za renderowanie tekstu na ekranie (czysty string)
func (m model) View() string {
	if !m.ready {
		return "\n  Inicjalizacja interfejsu..."
	}

	var s strings.Builder
	s.WriteString(headerStyle.Render(fmt.Sprintf(" SCRIPT LAUNCHER (%s) ", strings.ToUpper(runtime.GOOS))) + "\n")

	// Renderowanie listy plików
	if len(m.choices) == 0 {
		s.WriteString(fmt.Sprintf("  Brak plików %s w tym folderze.\n", m.extension))
	} else {
		for i, choice := range m.choices {
			cursor := "  "
			line := choice
			if m.cursor == i {
				cursor = "> "
				if !m.focusLogs {
					line = selectedStyle.Render(choice)
				} else {
					// Gdy przeglądamy logi, lista plików jest wyszarzona
					line = lipgloss.NewStyle().Foreground(lipgloss.Color("#aaaaaa")).Render(choice)
				}
			}
			s.WriteString(fmt.Sprintf("%s%s\n", cursor, line))
		}
	}

	// Styl panelu logów
	vStyle := viewportStyle
	logHeader := " LOGI TERMINALA (AUTO-ZAPIS DO .LOG) "
	if m.focusLogs {
		vStyle = activeViewportStyle
		logHeader = " TRYB PRZEGLĄDANIA LOGÓW (Q = POWRÓT) "
	}

	s.WriteString("\n" + headerStyle.Copy().Background(lipgloss.Color("#333333")).Render(logHeader) + "\n")
	s.WriteString(vStyle.Render(m.viewport.View()) + "\n")

	// Dynamiczna pomoc na dole ekranu
	help := " q: wyjdź • enter: uruchom • ↑/↓: nawigacja • myszka: scroll"
	if m.focusLogs {
		help = " q: powrót do listy • ↑/↓: przewijanie logów"
	}
	s.WriteString(helpStyle.Render(help))

	return s.String()
}

// Globalna zmienna p pozwala asynchronicznym funkcjom (goroutines) wysyłać dane do UI
var p *tea.Program

func main() {
	m := initialModel()

	// WithAltScreen: aplikacja działa w osobnym buforze (jak Vim/Nano)
	// WithMouseCellMotion: umożliwia przewijanie logów kółkiem myszy
	p = tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())

	if _, err := p.Run(); err != nil {
		fmt.Printf("Wystąpił błąd: %v", err)
		os.Exit(1)
	}
}
