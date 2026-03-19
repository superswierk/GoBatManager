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
// Definiujemy style raz, aby zachować spójność w całej aplikacji
var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(lipgloss.Color("#7D56F4")).
			Padding(0, 1)

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
// Bubble Tea używa typów do komunikacji między procesami tła a główną pętlą UI
type logLineMsg string               // Przesyła nową linię tekstu do wyświetlenia
type finishedMsg struct{ err error } // Informuje o zakończeniu działania skryptu

// --- MODEL APLIKACJI ---
// Główna struktura przechowująca stan aplikacji
type model struct {
	choices   []string       // Lista znalezionych skryptów (.bat/.sh)
	cursor    int            // Aktualnie wybrany element na liście
	viewport  viewport.Model // Komponent do obsługi przewijanego tekstu logów
	logLines  []string       // Historia wszystkich odebranych linii logów
	running   bool           // Czy skrypt jest aktualnie uruchomiony
	focusLogs bool           // Czy sterowanie klawiaturą dotyczy logów (scroll)
	ready     bool           // Czy interfejs został zainicjalizowany (rozmiar okna)
	extension string         // Rozszerzenie specyficzne dla systemu (.bat lub .sh)
	width     int            // Zapamiętana szerokość okna
	height    int            // Zapamiętana wysokość okna
}

// Inicjalizacja modelu i wykrywanie systemu operacyjnego
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
		choices:   scripts,
		extension: ext,
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

// Update - główna pętla obsługująca zdarzenia i logikę aplikacji
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	switch msg := msg.(type) {
	// Obsługa zdarzeń myszy (np. scrollowanie logów)
	case tea.MouseMsg:
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)

	// Dopasowanie układu przy zmianie rozmiaru terminala
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		// Podział okna: Lewy panel (lista) zajmuje ok. 1/3 szerokości
		leftWidth := msg.Width / 3
		if leftWidth < 28 {
			leftWidth = 28
		}

		// Obliczanie wymiarów dla viewportu logów
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
			return m, tea.Quit
		case "q", "esc":
			// Jeśli fokus na logach, wróć do listy. Jeśli nie, zamknij program.
			if m.focusLogs {
				m.focusLogs = false
				return m, nil
			}
			return m, tea.Quit
		case "up", "k":
			if m.focusLogs {
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
			// Uruchomienie skryptu, jeśli żaden inny nie pracuje
			if !m.running && len(m.choices) > 0 {
				m.running = true
				m.focusLogs = false
				m.logLines = []string{"[SYSTEM] Uruchamianie: " + m.choices[m.cursor] + "..."}
				m.viewport.SetContent(strings.Join(m.logLines, "\n"))
				target := m.choices[m.cursor]
				return m, m.runScript(target)
			}
		}

	// Nowa linia tekstu przyszła z procesu skryptu
	case logLineMsg:
		m.logLines = append(m.logLines, string(msg))
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
		m.viewport.GotoBottom() // Automatyczny scroll na dół

	// Powiadomienie o zakończeniu pracy procesu
	case finishedMsg:
		m.running = false
		m.focusLogs = true // Pozwól użytkownikowi przeglądać logi po zakończeniu
		status := "SUKCES"
		if msg.err != nil {
			status = fmt.Sprintf("BŁĄD (%v)", msg.err)
		}
		m.logLines = append(m.logLines, fmt.Sprintf("\n[SYSTEM] Proces zakończony: %s", status))
		m.logLines = append(m.logLines, "[SYSTEM] Naciśnij 'q' aby wrócić do listy skryptów.")
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
		m.viewport.GotoBottom()
	}

	return m, tea.Batch(cmds...)
}

// runScript - asynchroniczne wywołanie skryptu z zapisem do pliku i streamingiem do UI
func (m model) runScript(filename string) tea.Cmd {
	return func() tea.Msg {
		logFilename := strings.TrimSuffix(filename, m.extension) + ".log"

		// Otwieramy plik logu (nadpisuje istniejący)
		logFile, err := os.Create(logFilename)
		if err != nil {
			return finishedMsg{err: fmt.Errorf("błąd pliku log: %w", err)}
		}
		defer logFile.Close()

		var c *exec.Cmd
		if runtime.GOOS == "windows" {
			c = exec.Command("cmd", "/c", filename)
		} else {
			c = exec.Command("sh", filename)
		}

		// Łączymy wyjście standardowe i błędy
		stdout, _ := c.StdoutPipe()
		stderr, _ := c.StderrPipe()
		scriptOutput := io.MultiReader(stdout, stderr)

		if err := c.Start(); err != nil {
			return finishedMsg{err: fmt.Errorf("błąd startu: %w", err)}
		}

		// TeeReader: przesyła dane jednocześnie do logFile i do czytnika UI
		teeReader := io.TeeReader(scriptOutput, logFile)
		
		// bufio.Reader radzi sobie z liniami dowolnej długości (rozwiązuje problem 'token too long')
		reader := bufio.NewReader(teeReader)
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				// Wysyłamy linię do UI po usunięciu znaków końca linii
				p.Send(logLineMsg(strings.TrimRight(line, "\r\n")))
			}
			if err != nil {
				if err != io.EOF {
					p.Send(logLineMsg(fmt.Sprintf("[SYSTEM BŁĄD] %v", err)))
				}
				break
			}
		}

		err = c.Wait()
		return finishedMsg{err: err}
	}
}

// View - buduje końcowy obraz interfejsu z klocków (paneli)
func (m model) View() string {
	if !m.ready {
		return "\n  Inicjalizacja..."
	}

	// 1. NAGŁÓWEK
	header := headerStyle.Render(fmt.Sprintf(" SCRIPT LAUNCHER (%s) ", strings.ToUpper(runtime.GOOS)))

	// Obliczanie szerokości panelu bocznego
	leftWidth := m.width / 3
	if leftWidth < 28 {
		leftWidth = 28
	}

	// 2. LEWY PANEL (Lista skryptów)
	var leftBuilder strings.Builder
	leftTitle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4")).Render("DOSTĘPNE SKRYPTY:")
	leftBuilder.WriteString(leftTitle + "\n\n")

	if len(m.choices) == 0 {
		leftBuilder.WriteString(fmt.Sprintf("Brak plików %s.\n", m.extension))
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
			leftBuilder.WriteString(fmt.Sprintf("%s%s\n", cursor, line))
		}
	}

	leftBox := lipgloss.NewStyle().
		Width(leftWidth).
		Height(m.height - 3).
		PaddingRight(2).
		Render(leftBuilder.String())

	// 3. PRAWY PANEL (Logi)
	vStyle := viewportStyle
	logHeader := " LOGI TERMINALA (ZAPIS DO .LOG) "
	if m.focusLogs {
		vStyle = activeViewportStyle
		logHeader = " TRYB PRZEGLĄDANIA LOGÓW (Q = WRÓĆ) "
	}

	rightTitle := lipgloss.NewStyle().
		Background(lipgloss.Color("#333333")).
		Foreground(lipgloss.Color("#FFFFFF")).
		Padding(0, 1).
		Render(logHeader)

	// Łączenie tytułu logów z oknem logów w pionie
	rightBox := lipgloss.JoinVertical(lipgloss.Left,
		rightTitle,
		vStyle.Render(m.viewport.View()),
	)

	// 4. ŁĄCZENIE PANELI W POZIOMIE
	mainContent := lipgloss.JoinHorizontal(lipgloss.Top, leftBox, rightBox)

	// 5. STOPKA Z POMOCĄ
	help := " q: wyjdź • enter: uruchom • ↑/↓: nawigacja • myszka: scroll"
	if m.focusLogs {
		help = " q: powrót do listy • ↑/↓: przewijanie logów"
	}
	footer := helpStyle.Render(help)

	// Złożenie wszystkiego w jedną całość
	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		"", // Odstęp
		mainContent,
		footer,
	)
}

var p *tea.Program

func main() {
	m := initialModel()
	// AltScreen tworzy "czysty" terminal dla aplikacji, który znika po wyjściu
	p = tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())

	if _, err := p.Run(); err != nil {
		fmt.Printf("Błąd: %v", err)
		os.Exit(1)
	}
}
