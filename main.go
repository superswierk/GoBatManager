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

// --- ZMIENNE GLOBALNE ---
// Ta zmienna zostanie uzupełniona automatycznie przez GoReleaser podczas budowania taga.
// W trybie deweloperskim (go run .) będzie wyświetlać "dev".
var version = "dev"

// --- STYLE WIZUALNE (LIP GLOSS) ---
// Definiujemy style raz, aby używać ich wielokrotnie w całej aplikacji.
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

	// Ramka zmieniająca kolor na fioletowy, gdy użytkownik przewija logi (focus)
	activeViewportStyle = viewportStyle.Copy().
				BorderForeground(lipgloss.Color("#7D56F4"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))
)

// --- TYPY WIADOMOŚCI (MESSAGES) ---
// Bubble Tea używa typów do komunikacji między procesami tła a główną pętlą UI.
type logLineMsg string               // Przesyła nową linię tekstu do wyświetlenia w UI
type finishedMsg struct{ err error } // Informuje o zakończeniu działania skryptu

// --- MODEL APLIKACJI ---
// Główna struktura przechowująca stan całej aplikacji.
type model struct {
	choices   []string       // Lista znalezionych skryptów (.bat lub .sh)
	cursor    int            // Indeks aktualnie podświetlonego pliku na liście
	viewport  viewport.Model // Komponent Bubbles do obsługi przewijanego tekstu logów
	logLines  []string       // Bufor przechowujący wszystkie odebrane linie logów
	running   bool           // Czy skrypt jest aktualnie uruchomiony
	focusLogs bool           // Czy sterowanie (strzałki) jest przekierowane na logi
	ready     bool           // Czy otrzymaliśmy WindowSizeMsg i zainicjowaliśmy wymiary
	extension string         // Wykryte rozszerzenie specyficzne dla systemu
	width     int            // Zapamiętana szerokość okna terminala
	height    int            // Zapamiętana wysokość okna terminala
}

// Funkcja inicjalizująca model startowy i wykrywająca system.
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

// Init wywoływane na starcie aplikacji.
func (m model) Init() tea.Cmd {
	return nil
}

// Update - główna pętla obsługująca zdarzenia (klawisze, mysz, wiadomości systemowe).
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	switch msg := msg.(type) {
	// Obsługa myszy (scrollowanie logów)
	case tea.MouseMsg:
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)

	// WindowSizeMsg przychodzi na starcie i przy każdej zmianie rozmiaru okna.
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		// Podział okna: Lewy panel (lista) zajmuje ok. 1/3 szerokości.
		leftWidth := msg.Width / 3
		if leftWidth < 28 {
			leftWidth = 28 // Minimalna szerokość Sidebaru
		}

		// Obliczanie wymiarów dla viewportu logów (prawa strona).
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
			// Jeśli przeglądamy logi, 'q' wraca do menu. W przeciwnym razie zamyka aplikację.
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
			// Uruchomienie skryptu, jeśli żaden inny nie pracuje.
			if !m.running && len(m.choices) > 0 {
				m.running = true
				m.focusLogs = false
				m.logLines = []string{"[SYSTEM] Uruchamianie: " + m.choices[m.cursor] + "..."}
				m.viewport.SetContent(strings.Join(m.logLines, "\n"))
				target := m.choices[m.cursor]
				return m, m.runScript(target)
			}
		}

	// Nowa linia tekstu przyszła z procesu skryptu (goroutine).
	case logLineMsg:
		m.logLines = append(m.logLines, string(msg))
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
		m.viewport.GotoBottom() // Automatyczny scroll przy nowych logach

	// Powiadomienie o zakończeniu pracy procesu skryptu.
	case finishedMsg:
		m.running = false
		m.focusLogs = true // Aktywujemy tryb przeglądania logów (scroll) po zakończeniu.
		status := "SUKCES"
		if msg.err != nil {
			status = fmt.Sprintf("BŁĄD (%v)", msg.err)
		}
		m.logLines = append(m.logLines, fmt.Sprintf("\n[SYSTEM] Proces zakończony: %s", status))
		m.logLines = append(m.logLines, "[SYSTEM] Tryb przeglądania logów aktywny. Naciśnij 'q' aby wrócić do listy.")
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
		m.viewport.GotoBottom()
	}

	return m, tea.Batch(cmds...)
}

// runScript - asynchroniczne uruchomienie procesu, zapis do pliku i streaming do UI.
func (m model) runScript(filename string) tea.Cmd {
	return func() tea.Msg {
		logFilename := strings.TrimSuffix(filename, m.extension) + ".log"

		// Tworzymy lub nadpisujemy plik .log dla danego skryptu.
		logFile, err := os.Create(logFilename)
		if err != nil {
			return finishedMsg{err: fmt.Errorf("błąd zapisu pliku .log: %w", err)}
		}
		defer logFile.Close()

		// Dobór komendy w zależności od wykrytego systemu.
		var c *exec.Cmd
		if runtime.GOOS == "windows" {
			c = exec.Command("cmd", "/c", filename)
		} else {
			c = exec.Command("sh", filename)
		}

		// Przechwytywanie standardowego wyjścia i błędów.
		stdout, _ := c.StdoutPipe()
		stderr, _ := c.StderrPipe()
		scriptOutput := io.MultiReader(stdout, stderr)

		if err := c.Start(); err != nil {
			return finishedMsg{err: fmt.Errorf("nie udało się uruchomić: %w", err)}
		}

		// TeeReader: jednocześnie zapisuje do pliku i pozwala czytać dane do interfejsu.
		teeReader := io.TeeReader(scriptOutput, logFile)
		
		// bufio.Reader radzi sobie z liniami dowolnej długości (rozwiązuje błąd 'token too long').
		reader := bufio.NewReader(teeReader)
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				// Wysyłamy linię do głównej pętli Bubble Tea (p.Send).
				p.Send(logLineMsg(strings.TrimRight(line, "\r\n")))
			}
			if err != nil {
				if err != io.EOF {
					p.Send(logLineMsg(fmt.Sprintf("[SYSTEM BŁĄD] Problem z odczytem: %v", err)))
				}
				break
			}
		}

		// Czekamy na fizyczne zakończenie procesu.
		err = c.Wait()
		return finishedMsg{err: err}
	}
}

// View - buduje końcowy obraz interfejsu (renderuje klocki Sidebaru i Logów).
func (m model) View() string {
	if !m.ready {
		return "\n  Inicjalizacja interfejsu..."
	}

	// 1. NAGŁÓWEK GŁÓWNY (z dynamiczną wersją)
	header := headerStyle.Render(fmt.Sprintf(" SCRIPT LAUNCHER (%s) v%s ", strings.ToUpper(runtime.GOOS), version))

	// Obliczanie szerokości lewego panelu.
	leftWidth := m.width / 3
	if leftWidth < 28 {
		leftWidth = 28
	}

	// 2. LEWY PANEL (Lista skryptów)
	var leftBuilder strings.Builder
	leftTitle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7D56F4")).Render("DOSTĘPNE SKRYPTY:")
	leftBuilder.WriteString(leftTitle + "\n\n")

	if len(m.choices) == 0 {
		leftBuilder.WriteString(fmt.Sprintf("Brak plików %s w tym folderze.\n", m.extension))
	} else {
		for i, choice := range m.choices {
			cursor := "  "
			line := choice
			if m.cursor == i {
				cursor = "> "
				if !m.focusLogs {
					line = selectedStyle.Render(choice)
				} else {
					// Gdy użytkownik przewija logi, sidebar jest przyciemniony.
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
	logHeader := " LOGI TERMINALA (AUTO-ZAPIS DO .LOG) "
	if m.focusLogs {
		vStyle = activeViewportStyle
		logHeader = " TRYB PRZEGLĄDANIA LOGÓW (Q = POWRÓT) "
	}

	// Tytuł dla prawego panelu.
	rightTitle := lipgloss.NewStyle().
		Background(lipgloss.Color("#333333")).
		Foreground(lipgloss.Color("#FFFFFF")).
		Padding(0, 1).
		Render(logHeader)

	// Składamy prawy panel: Tytuł nad ramką z tekstem (viewport).
	rightBox := lipgloss.JoinVertical(lipgloss.Left,
		rightTitle,
		vStyle.Render(m.viewport.View()),
	)

	// Łączymy lewy sidebar z prawymi logami w poziomie.
	mainContent := lipgloss.JoinHorizontal(lipgloss.Top, leftBox, rightBox)

	// 4. STOPKA (Dynamiczna pomoc)
	help := " q: wyjdź • enter: uruchom • ↑/↓: nawigacja • myszka: scroll"
	if m.focusLogs {
		help = " q: powrót do listy • ↑/↓: przewijanie logów kółkiem myszy"
	}
	footer := helpStyle.Render(help)

	// 5. CAŁOŚĆ: Składamy wszystko w pionie (Nagłówek -> Miejsce -> Treść -> Stopka).
	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		"", // Pusta linia odstępu
		mainContent,
		footer,
	)
}

// Globalna zmienna p pozwala asynchronicznym funkcjom (goroutines) wysyłać dane do UI.
var p *tea.Program

func main() {
	m := initialModel()
	// AltScreen zapobiega zaśmiecaniu historii terminala.
	// MouseCellMotion umożliwia scrollowanie kółkiem myszy.
	p = tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())

	if _, err := p.Run(); err != nil {
		fmt.Printf("Wystąpił błąd krytyczny: %v", err)
		os.Exit(1)
	}
}
