package main

import (
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"
)

const barWidth = 30

type session struct {
	amount    int
	duration  int
	mint      string
	guest     string
	startTime time.Time
}

var keyChan = make(chan byte, 256)
var readErr = make(chan struct{})
var currentMode = "menu"

func init() {
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil || n == 0 {
				close(readErr)
				return
			}
			keyChan <- buf[0]
		}
	}()
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func loadSession() *session {
	return &session{
		amount:    atoi(os.Getenv("TOLLGATE_AMOUNT")),
		duration:  atoi(os.Getenv("TOLLGATE_DURATION")),
		mint:      os.Getenv("TOLLGATE_MINT"),
		guest:     os.Getenv("TOLLGATE_GUEST"),
		startTime: time.Unix(int64(atoi(os.Getenv("TOLLGATE_SESSION_START"))), 0),
	}
}

func (s *session) remaining() time.Duration {
	elapsed := time.Since(s.startTime)
	d := time.Duration(s.duration) * time.Second
	r := d - elapsed
	if r < 0 {
		return 0
	}
	return r
}

func (s *session) expired() bool {
	return s.remaining() <= 0
}

func readByte() byte {
	select {
	case b := <-keyChan:
		return b
	case <-readErr:
		return 0
	}
}

func readLine() (string, bool) {
	var buf []byte
	for {
		b := readByte()
		if b == 0 {
			return "", false
		}
		if b == '\n' || b == '\r' {
			return strings.TrimSpace(string(buf)), true
		}
		if b == 0x7f || b == 0x08 {
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
			}
			continue
		}
		buf = append(buf, b)
	}
}

func drainKeys() {
	for {
		select {
		case <-keyChan:
		default:
			return
		}
	}
}

func printBanner(s *session) {
	mintDisplay := s.mint
	if len(mintDisplay) > 36 {
		mintDisplay = mintDisplay[:33] + "..."
	}
	testStr := "NO"
	if strings.Contains(strings.ToLower(s.mint), "test") {
		testStr = "YES"
	}
	minutes := s.duration / 60

	fmt.Println()
	fmt.Println("  +======================================+")
	fmt.Println("  |        CASHU TOLLGATE                |")
	fmt.Println("  +======================================+")
	fmt.Printf("  |  Mint:   %-28s |\n", mintDisplay)
	fmt.Printf("  |  Amount: %4d %-23s |\n", s.amount, "sat")
	fmt.Printf("  |  Time:   %4d min (%5d sec)       |\n", minutes, s.duration)
	fmt.Printf("  |  User:   %-28s |\n", s.guest)
	fmt.Printf("  |  Test:   %-28s |\n", testStr)
	fmt.Println("  +======================================+")
	fmt.Println()
}

func showMenu(s *session) {
	minutes := s.duration / 60
	if minutes < 1 {
		minutes = 1
	}
	fmt.Printf("  Welcome, %s. You have %d minutes.\n\n", s.guest, minutes)
	fmt.Println("  [1] Timer - watch your session count down")
	fmt.Println("  [2] Adventure - explore the Crypt of Cashu")
	fmt.Println("  [q] Quit")
	fmt.Print("\n  Choice: ")
}

func timerMode(s *session) {
	currentMode = "timer"
	defer func() {
		currentMode = "menu"
		drainKeys()
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	fmt.Println()
	fmt.Printf("  Mint: %s\n", s.mint)
	fmt.Printf("  Paid for: %d minutes\n", s.duration/60)
	fmt.Println("  (press 'q' + Enter to return to menu)")
	fmt.Println()

	for {
		if s.expired() {
			fmt.Println("\n\n  Time's up! Session ending.")
			os.Exit(0)
		}

		rem := s.remaining()
		mins := int(rem.Minutes())
		secs := int(rem.Seconds()) % 60
		pct := float64(rem) / float64(time.Duration(s.duration)*time.Second)
		filled := int(pct * float64(barWidth))

		bar := strings.Repeat("#", filled) + strings.Repeat("-", barWidth-filled)
		fmt.Printf("\r  [%s]  %dm %02ds remaining          ", bar, mins, secs)

		select {
		case <-ticker.C:
		case b := <-keyChan:
			if b == 'q' || b == 'Q' || b == 3 {
				fmt.Println()
				return
			}
		}
	}
}

type roomKind int

const (
	roomMonster roomKind = iota
	roomTreasure
	roomEmpty
)

type room struct {
	kind     roomKind
	monster  string
	resolved string
}

var monsters = []string{
	"Satoshi Goblin",
	"Lightning Elemental",
	"Mint Golem",
	"Fee Bandit",
	"Confirmation Banshee",
	"RBF Wraith",
}

var emptyFlavors = []string{
	"The chamber is empty. Cryptographic dust drifts in the dim light.",
	"Collapsed mining rigs line the walls. Nothing of value here.",
	"An empty block template. You move through quickly.",
	"Silence. The room holds only echoes of past transactions.",
}

func adventureMode(s *session) {
	currentMode = "adventure"
	defer func() { currentMode = "menu" }()

	if s.amount < 1 {
		fmt.Println("\n  Not enough satoshis for an adventure!")
		return
	}

	hp := s.amount
	rooms := make([]room, s.amount)
	for i := range rooms {
		roll := rand.Intn(100)
		if roll < 50 {
			rooms[i] = room{
				kind:    roomMonster,
				monster: monsters[rand.Intn(len(monsters))],
			}
		} else if roll < 75 {
			rooms[i] = room{kind: roomTreasure}
		} else {
			rooms[i] = room{kind: roomEmpty}
		}
	}

	currentRoom := 0
	enterRoom(&rooms[currentRoom], &hp)

	fmt.Println()
	fmt.Println("  === The Crypt of Cashu ===")
	fmt.Printf("  HP: %d  |  Room: %d/%d\n", hp, currentRoom+1, len(rooms))
	fmt.Printf("  %s\n", rooms[currentRoom].resolved)

	for {
		if hp <= 0 {
			fmt.Println("\n  You died! The dungeon claims another soul.")
			fmt.Println("\n  [Press Enter to return to menu]")
			readLine()
			return
		}

		if s.expired() {
			fmt.Println("\n\n  The dungeon collapses around you! Time has run out.")
			os.Exit(0)
		}

		fmt.Print("\n  > ")
		cmd, ok := readLine()
		if !ok {
			return
		}
		cmd = strings.ToLower(cmd)

		switch {
		case cmd == "advance" || cmd == "a":
			currentRoom++
			if currentRoom >= len(rooms) {
				fmt.Println("\n  Victory! You conquered the Crypt of Cashu!")
				fmt.Println("\n  [Press Enter to return to menu]")
				readLine()
				return
			}
			enterRoom(&rooms[currentRoom], &hp)
			fmt.Printf("  HP: %d  |  Room: %d/%d\n", hp, currentRoom+1, len(rooms))
			fmt.Printf("  %s\n", rooms[currentRoom].resolved)
		case cmd == "look" || cmd == "l":
			fmt.Printf("  %s\n", rooms[currentRoom].resolved)
		case cmd == "status" || cmd == "s":
			fmt.Printf("  HP: %d  |  Room: %d/%d\n", hp, currentRoom+1, len(rooms))
		case cmd == "flee" || cmd == "q":
			fmt.Println("  You flee the dungeon!")
			return
		default:
			fmt.Println("  Commands: look, advance, status, flee")
		}
	}
}

func enterRoom(r *room, hp *int) {
	switch r.kind {
	case roomMonster:
		pRoll := rand.Intn(6) + 1
		mRoll := rand.Intn(6) + 1
		if mRoll >= pRoll {
			*hp--
			r.resolved = fmt.Sprintf("A %s strikes! You roll %d, it rolls %d. You lose 1 HP!", r.monster, pRoll, mRoll)
		} else {
			r.resolved = fmt.Sprintf("A %s attacks! You roll %d, it rolls %d. You defeat it!", r.monster, pRoll, mRoll)
		}
	case roomTreasure:
		*hp++
		r.resolved = "A glittering cache of satoshis! You pocket the coins. +1 HP"
	case roomEmpty:
		r.resolved = emptyFlavors[rand.Intn(len(emptyFlavors))]
	}
}

func startExpiryWatcher(s *session) {
	go func() {
		for {
			if s.expired() {
				switch currentMode {
				case "adventure":
					fmt.Println("\n\n  The dungeon collapses around you! Time has run out.")
				default:
					fmt.Println("\n\n  Time's up! Session ending.")
				}
				os.Exit(0)
			}
			time.Sleep(time.Second)
		}
	}()
}

func main() {
	s := loadSession()

	if s.amount <= 0 || s.duration <= 0 {
		fmt.Println("Invalid session. Missing TOLLGATE environment variables.")
		os.Exit(1)
	}

	startExpiryWatcher(s)
	printBanner(s)

	for {
		if s.expired() {
			fmt.Println("\n  Time's up! Session ending.")
			os.Exit(0)
		}

		showMenu(s)
		choice, ok := readLine()
		if !ok {
			return
		}

		switch choice {
		case "1":
			timerMode(s)
		case "2":
			adventureMode(s)
		case "q", "Q", "quit", "exit":
			fmt.Println("  Goodbye!")
			return
		default:
			fmt.Println("  Invalid choice. Enter 1, 2, or q.")
		}
	}
}
