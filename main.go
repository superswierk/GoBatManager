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
// Ta zmienna zostanie uzupełniona automatycznie przez GoReleaser podczas budowania taga.
// W trybie deweloperskim (go run .) będzie wyświetlać "dev".
var version = "dev"

// Plik naszej bazy danych w formacie JSON
const timesFile = "script_times.json"

// --- FUNKCJE POMOCNICZE BAZY DANYCH (JSON) ---
// Ładuje zapisane czasy z pliku JSON do mapy. Jeśli plik nie istnieje, zwraca pustą mapę.
func loadTimes() map[string]string {
	times := make(map[string]string)
	data, err := os.ReadFile(timesFile)
	if err == nil {
		json.Unmarshal(data, &times)
	}
	return times
}

// Zapisuje mapę czasów do pliku JSON w ładnie sformatowany sposób (Indent).
func saveTimes(times map[string]string) {
	data, err := json.MarshalIndent(times, "", "  ")
	if err == nil {
		os.WriteFile(timesFile, data, 0644)
	}
}

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

	// Styl dla zaznaczonych elementów (multi-select)
	checkedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#00CBCB")).
			Bold(true)

	// Styl sukcesu (zielony)
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF00"))
	// Styl błędu (czerwony)
	errorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF0000"))

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

	// Styl dla licznika czasu w nagłówku logów
	timerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFA500")).
			Bold(true)

	// Styl dla zapisanego czasu w liście skryptów
	savedTimeStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))

	// Styl dla okna podglądu skryptu
	previewViewportStyle = viewportStyle.Copy().
			BorderForeground(lipgloss.Color("#00BFFF"))
)

// --- TYPY WIADOMOŚCI (MESSAGES) ---
// Bubble Tea używa typów do komunikacji między procesami tła a główną pętlą UI.
type logLineMsg string               // Przesyła nową linię tekstu do wyświetlenia w UI
type tickMsg time.Time              // Wiadomość do aktualizacji licznika czasu

// Informuje o zakończeniu działania skryptu, przechowując ewentualny błąd i nazwę skryptu
type finishedMsg struct {
	err  error
	name string
}

// --- MODEL APLIKACJI ---
// Główna struktura przechowująca stan całej aplikacji.
type model struct {
	choices     []string          // Lista znalezionych skryptów (.bat lub .sh)
	cursor      int               // Indeks aktualnie podświetlonego pliku na liście
	selected    map[int]bool      // Mapa indeksów zaznaczonych skryptów (multi-select)
	results     map[string]bool   // Wyniki wykonania: true = sukces, false = błąd
	queue       []int             // Kolejka indeksów do wykonania
	viewport    viewport.Model    // Komponent Bubbles do obsługi przewijanego tekstu logów
	logLines    []string          // Bufor przechowujący wszystkie odebrane linie logów
	running     bool              // Czy skrypt jest aktualnie uruchomiony
	focusLogs   bool              // Czy sterowanie (strzałki) jest przekierowane na logi
	ready       bool              // Czy otrzymaliśmy WindowSizeMsg i zainicjowaliśmy wymiary
	extension   string            // Wykryte rozszerzenie specyficzne dla systemu
	width       int               // Zapamiętana szerokość okna terminala
	height      int               // Zapamiętana wysokość okna terminala
	startTime   time.Time         // Czas rozpoczęcia skryptu
	elapsed     time.Duration     // Aktualny czas trwania
	scriptTimes map[string]string // Baza danych przechowująca ostatnie czasy działania (Nazwa -> Czas)
	previewing  bool              // Czy włączony jest tryb podglądu skryptu
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
		choices:     scripts,
		extension:   ext,
		selected:    make(map[int]bool),
		results:     make(map[string]bool),
		scriptTimes: loadTimes(), // Wczytanie historii czasów na starcie
	}
}

// Init wywoływane na starcie aplikacji.
func (m model) Init() tea.Cmd {
	return nil
}

// tick co sekundę do odświeżania stopera w czasie rzeczywistym
func tick() tea.Cmd {
	return tea.Every(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
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

	// Aktualizacja licznika czasu (jeśli skrypt nadal pracuje)
	case tickMsg:
		if m.running {
			m.elapsed = time.Since(m.startTime)
			return m, tick()
		}

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
			// Jeśli przeglądamy podgląd, zamykamy go
			if m.previewing {
				m.previewing = false
				if len(m.logLines) == 0 {
					m.viewport.SetContent("Wybierz skrypt i naciśnij ENTER...")
				} else {
					m.viewport.SetContent(strings.Join(m.logLines, "\n"))
					m.viewport.GotoBottom()
				}
				return m, nil
			}
			// Jeśli przeglądamy logi, 'q' wraca do menu. W przeciwnym razie zamyka aplikację.
			if m.focusLogs {
				m.focusLogs = false
				return m, nil
			}
			return m, tea.Quit

		case " ":
			// Przełączanie trybu podglądu spacją
			if !m.running && len(m.choices) > 0 {
				m.previewing = !m.previewing
				if m.previewing {
					m = m.updatePreview()
				} else {
					if len(m.logLines) == 0 {
						m.viewport.SetContent("Wybierz skrypt i naciśnij ENTER...")
					} else {
						m.viewport.SetContent(strings.Join(m.logLines, "\n"))
						m.viewport.GotoBottom()
					}
				}
			}
			return m, nil

		case "x":
			// Zaznaczanie skryptu do kolejki (multi-select)
			if !m.running && len(m.choices) > 0 {
				m.selected[m.cursor] = !m.selected[m.cursor]
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
			// Uruchomienie skryptu lub kolejki
			if !m.running && len(m.choices) > 0 {
				m.running = true
				m.focusLogs = false
				m.previewing = false

				// Budowanie kolejki: zaznaczone lub tylko ten pod kursorem
				m.queue = []int{}
				for i := range m.choices {
					if m.selected[i] {
						m.queue = append(m.queue, i)
					}
				}
				if len(m.queue) == 0 {
					m.queue = append(m.queue, m.cursor)
				}

				return m.runNextInQueue()
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
		m.elapsed = time.Since(m.startTime)
		
		// Zapisanie czasu do bazy (format np. 12s, 1m5s) i zrzut do pliku
		timeFormatted := m.elapsed.Round(time.Second).String()
		m.scriptTimes[msg.name] = timeFormatted
		saveTimes(m.scriptTimes)
		
		status := "SUKCES"
		if msg.err != nil {
			status = fmt.Sprintf("BŁĄD (%v)", msg.err)
			m.results[msg.name] = false // Błąd w GUI
			// USUNIĘTO: m.queue = nil -> Kolejka będzie kontynuowana mimo błędu
		} else {
			m.results[msg.name] = true // Sukces w GUI
		}
		
		m.logLines = append(m.logLines, fmt.Sprintf("\n[SYSTEM] Proces zakończony: %s", status))
		m.logLines = append(m.logLines, fmt.Sprintf("[SYSTEM] Czas trwania: %s", timeFormatted))
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
		m.viewport.GotoBottom()

		// Jeśli są kolejne skrypty w kolejce, uruchom następny (nawet jeśli poprzedni padł)
		if len(m.queue) > 0 {
			m.running = true
			return m.runNextInQueue()
		}

		m.focusLogs = true // Po wszystkim aktywujemy tryb przeglądania logów
		m.logLines = append(m.logLines, "[SYSTEM] Wszystkie zadania z kolejki zostały przetworzone. Naciśnij 'q' aby wrócić.")
		m.viewport.SetContent(strings.Join(m.logLines, "\n"))
	}

	return m, tea.Batch(cmds...)
}

// runNextInQueue - pobiera następny skrypt z kolejki i go odpala
func (m model) runNextInQueue() (model, tea.Cmd) {
	if len(m.queue) == 0 {
		m.running = false
		return m, nil
	}

	idx := m.queue[0]
	m.queue = m.queue[1:]
	m.cursor = idx // Ustaw kursor na aktualnie wykonywanym zadaniu

	m.startTime = time.Now()
	m.elapsed = 0
	msg := fmt.Sprintf("[SYSTEM] >>> KOLEJKA: Uruchamianie %s...", m.choices[idx])
	m.logLines = append(m.logLines, "\n"+msg)
	m.viewport.SetContent(strings.Join(m.logLines, "\n"))
	
	target := m.choices[idx]
	return m, tea.Batch(m.runScript(target), tick())
}

// Funkcja ładująca zawartość pliku do podglądu
func (m model) updatePreview() model {
	if len(m.choices) == 0 {
		return m
	}
	filename := m.choices[m.cursor]
	content, err := os.ReadFile(filename)
	if err != nil {
		m.viewport.SetContent(fmt.Sprintf("Nie można odczytać pliku: %v", err))
		return m
	}
	m.viewport.SetContent(string(content))
	m.viewport.GotoTop()
	return m
}

// runScript - asynchroniczne uruchomienie procesu, zapis do pliku i streaming do UI.
func (m model) runScript(filename string) tea.Cmd {
	return func() tea.Msg {
		logFilename := strings.TrimSuffix(filename, m.extension) + ".log"

		// Tworzymy lub nadpisujemy plik .log dla danego skryptu.
		logFile, err := os.Create(logFilename)
		if err != nil {
			return finishedMsg{err: fmt.Errorf("błąd zapisu pliku .log: %w", err), name: filename}
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
			return finishedMsg{err: fmt.Errorf("nie udało się uruchomić: %w", err), name: filename}
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
		return finishedMsg{err: err, name: filename} // Zwracamy nazwę, by baza wiedziała, kto skończył
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
			
			// Ikona zaznaczenia (multi-select)
			checked := " [ ] "
			if m.selected[i] {
				checked = " [x] "
			}

			// Ikona wyniku (sukces/błąd)
			resultIcon := ""
			if res, ok := m.results[choice]; ok {
				if res {
					resultIcon = successStyle.Render("✔ ")
				} else {
					resultIcon = errorStyle.Render("✘ ")
				}
			}

			line := choice
			
			// Kolorowanie wiersza
			if m.cursor == i {
				cursor = "> "
				if !m.focusLogs {
					line = selectedStyle.Render(choice)
				} else {
					line = lipgloss.NewStyle().Foreground(lipgloss.Color("#aaaaaa")).Render(choice)
				}
			} else if m.selected[i] {
				line = checkedStyle.Render(choice)
			}
			
			// Jeśli mamy zapisany czas w JSON, doklejamy go obok nazwy skryptu
			timeBadge := ""
			if savedTime, exists := m.scriptTimes[choice]; exists {
				timeBadge = savedTimeStyle.Render(fmt.Sprintf(" (%s)", savedTime))
			}

			leftBuilder.WriteString(fmt.Sprintf("%s%s%s%s%s\n", cursor, resultIcon, checked, line, timeBadge))
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
	
	// Przygotowanie informacji o czasie pracy wyświetlanej w trakcie
	timeStr := ""
	if m.running || m.elapsed > 0 {
		timeStr = timerStyle.Render(fmt.Sprintf(" [%s] ", m.elapsed.Round(time.Second)))
	}

	if m.previewing {
		vStyle = previewViewportStyle
		logHeader = fmt.Sprintf(" PODGLĄD: %s (SPACJA = ZAMKNIJ) ", m.choices[m.cursor])
		timeStr = "" 
	} else if m.focusLogs {
		vStyle = activeViewportStyle
		logHeader = " TRYB PRZEGLĄDANIA LOGÓW (Q = POWRÓT) "
	}

	// Tytuł dla prawego panelu z dołączonym licznikiem czasu.
	rightTitle := lipgloss.NewStyle().
		Background(lipgloss.Color("#333333")).
		Foreground(lipgloss.Color("#FFFFFF")).
		Padding(0, 1).
		Render(logHeader)
	
	// Składamy nagłówek panelu logów (Tytuł + Czas)
	logBar := lipgloss.JoinHorizontal(lipgloss.Center, rightTitle, timeStr)

	// Składamy prawy panel: Tytuł nad ramką z tekstem (viewport).
	rightBox := lipgloss.JoinVertical(lipgloss.Left,
		logBar,
		vStyle.Render(m.viewport.View()),
	)

	// Łączymy lewy sidebar z prawymi logami w poziomie.
	mainContent := lipgloss.JoinHorizontal(lipgloss.Top, leftBox, rightBox)

	// 4. STOPKA (Dynamiczna pomoc)
	help := " q: wyjdź • x: zaznacz • enter: uruchom (kolejkę) • spacja: podgląd • ↑/↓: nawigacja"
	if m.previewing {
		help = " spacja/q: zamknij podgląd • ↑/↓: nawigacja • myszka: przewijanie podglądu"
	} else if m.focusLogs {
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
