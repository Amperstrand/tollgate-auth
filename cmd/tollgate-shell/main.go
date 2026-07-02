package main

import (
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"
)

// ─── ANSI escape codes ──────────────────────────────────────────

const (
	clrScreen   = "\033[2J"
	clrFromDown = "\033[0J"
	cursorHome  = "\033[1;1H"
	hideCursor  = "\033[?25l"
	showCursor  = "\033[?25h"
	saveCursor  = "\033[s"
	restCursor  = "\033[u"
	reset       = "\033[0m"
	bold        = "\033[1m"
	dim         = "\033[2m"
	revVid      = "\033[7m"
)

const termRows = 24
const termCols = 80
const playRows = 23 // scroll region bottom for text games

// ─── Session ────────────────────────────────────────────────────

type session struct {
	amount    int
	duration  int
	mint      string
	guest     string
	startTime time.Time
}

var keyChan = make(chan byte, 256)
var readErr = make(chan struct{})
var currentMode = "arcade"

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

// ─── Key reading ────────────────────────────────────────────────

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

// ─── Screen helpers ─────────────────────────────────────────────

func move(r, c int)    { fmt.Printf("\033[%d;%dH", r, c) }
func clearLine(r int)  { fmt.Printf("\033[%d;1H\033[2K", r) }
func setScrollRegion() { fmt.Printf("\033[1;%dr", playRows) }
func fullScreen()      { fmt.Print("\033[r") }

// truncateMint shortens mint URL for display.
func truncateMint(s string) string {
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	if len(s) > 28 {
		return s[:25] + "..."
	}
	return s
}

// statusBarLine draws the always-on status bar at the bottom row.
func statusBarLine(s *session) {
	rem := s.remaining()
	mins := int(rem.Minutes())
	secs := int(rem.Seconds()) % 60
	bar := fmt.Sprintf(" \u23f1 %dm %02ds \u2502 %d sat \u2502 %s \u2502 %s ",
		mins, secs, s.amount, truncateMint(s.mint), s.guest)
	for len(bar) < termCols {
		bar += " "
	}
	if len(bar) > termCols {
		bar = bar[:termCols]
	}
	fmt.Print(saveCursor)
	fmt.Printf("\033[%d;1H", termRows)
	fmt.Print(revVid, bar, reset)
	fmt.Print(restCursor)
}

// startStatusBar launches a goroutine that refreshes the status bar.
func startStatusBar(s *session, stop <-chan struct{}) {
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				statusBarLine(s)
			case <-stop:
				return
			}
			if s.expired() {
				return
			}
		}
	}()
}

// ─── Expiry watcher ─────────────────────────────────────────────

func startExpiryWatcher(s *session) {
	go func() {
		for {
			if s.expired() {
				fullScreen()
				fmt.Print(showCursor)
				fmt.Print(clrScreen, cursorHome)
				switch currentMode {
				case "snake":
					fmt.Println("\n  GAME OVER — time expired!")
				case "crypt":
					fmt.Println("\n  The crypt collapses! Time has run out.")
				default:
					fmt.Println("\n  Time's up! Session ending.")
				}
				fmt.Println("\n  Thanks for playing at tollgate-auth.")
				fmt.Println("  Ecash for infrastructure access — cashu.space")
				os.Exit(0)
			}
			time.Sleep(time.Second)
		}
	}()
}

// ─── Banner ─────────────────────────────────────────────────────

func showBanner(s *session) {
	mintDisplay := truncateMint(s.mint)
	minutes := s.duration / 60
	if minutes < 1 {
		minutes = 1
	}

	fmt.Print(hideCursor)
	fmt.Print(clrScreen, cursorHome)

	fmt.Println()
	fmt.Println("  " + bold + "\u2554\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2551" + reset)
	fmt.Println("  " + bold + "\u2551          CASHU TOLLGATE ARCADE           \u2551" + reset)
	fmt.Println("  " + bold + "\u2560\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2551" + reset)
	fmt.Printf("  "+bold+"\u2551  %-38s \u2551"+reset+"\n", fmt.Sprintf("Mint:   %s", mintDisplay))
	fmt.Printf("  "+bold+"\u2551  %-38s \u2551"+reset+"\n", fmt.Sprintf("Amount: %d sat", s.amount))
	fmt.Printf("  "+bold+"\u2551  %-38s \u2551"+reset+"\n", fmt.Sprintf("Time:   %d min (%d s)", minutes, s.duration))
	fmt.Printf("  "+bold+"\u2551  %-38s \u2551"+reset+"\n", fmt.Sprintf("Guest:  %s", s.guest))
	fmt.Println("  " + bold + "\u255a\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2559" + reset)
	fmt.Println()
	fmt.Println("  " + dim + "Your ecash pays for game time." + reset)
	fmt.Println("  " + dim + "10 seconds per satoshi — play hard." + reset)

	time.Sleep(2500 * time.Millisecond)
}

// ─── Arcade picker ──────────────────────────────────────────────

func arcadePicker(s *session) string {
	currentMode = "arcade"
	drainKeys()
	setScrollRegion()
	fmt.Print(clrScreen, cursorHome)

	fmt.Println()
	fmt.Println("  " + bold + "\u2554\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2551" + reset)
	fmt.Println("  " + bold + "\u2551         TOLLGATE ARCADE              \u2551" + reset)
	fmt.Println("  " + bold + "\u2560\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2551" + reset)
	fmt.Println("  " + bold + "\u2551                                      \u2551" + reset)
	fmt.Println("  " + bold + "\u2551  [1]  Crypt of Cashu                 \u2551" + reset)
	fmt.Println("  " + bold + "\u2551       Explore a dungeon, fight       \u2551" + reset)
	fmt.Println("  " + bold + "\u2551       monsters, find treasure.       \u2551" + reset)
	fmt.Println("  " + bold + "\u2551                                      \u2551" + reset)
	fmt.Println("  " + bold + "\u2551  [2]  Snake                          \u2551" + reset)
	fmt.Println("  " + bold + "\u2551       Eat the sats, don\u2019t hit        \u2551" + reset)
	fmt.Println("  " + bold + "\u2551       the walls. Arrow keys.         \u2551" + reset)
	fmt.Println("  " + bold + "\u2551                                      \u2551" + reset)
	fmt.Println("  " + bold + "\u2551  [3]  Session Timer                  \u2551" + reset)
	fmt.Println("  " + bold + "\u2551       Watch the countdown bar.       \u2551" + reset)
	fmt.Println("  " + bold + "\u2551                                      \u2551" + reset)
	fmt.Println("  " + bold + "\u2551  [q]  Quit                           \u2551" + reset)
	fmt.Println("  " + bold + "\u2551                                      \u2551" + reset)
	fmt.Println("  " + bold + "\u255a\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2559" + reset)
	fmt.Println()
	fmt.Print("  Choose your game: ")

	for {
		choice, ok := readLine()
		if !ok {
			return "quit"
		}
		switch choice {
		case "1":
			return "crypt"
		case "2":
			return "snake"
		case "3":
			return "timer"
		case "q", "Q", "quit", "exit":
			return "quit"
		default:
			fmt.Println("  Pick 1, 2, 3 or q.")
			fmt.Print("  Choose your game: ")
		}
	}
}

// ─── Timer mode ─────────────────────────────────────────────────

func timerMode(s *session) {
	currentMode = "timer"
	defer drainKeys()

	setScrollRegion()
	fmt.Print(clrScreen, cursorHome)

	fmt.Println()
	fmt.Printf("  " + bold + "Session Timer" + reset + "\n")
	fmt.Printf("  Mint: %s\n", s.mint)
	fmt.Printf("  Paid for: %d seconds (%d min)\n\n", s.duration, s.duration/60)
	fmt.Println("  " + dim + "(press q + Enter to return to arcade)" + reset)
	fmt.Println()

	barWidth := 40
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		if s.expired() {
			return
		}
		rem := s.remaining()
		mins := int(rem.Minutes())
		secs := int(rem.Seconds()) % 60
		pct := float64(rem) / float64(time.Duration(s.duration)*time.Second)
		filled := int(pct * float64(barWidth))

		bar := strings.Repeat("\u2588", filled) + strings.Repeat("\u2591", barWidth-filled)
		fmt.Printf("\r  %s  %dm %02ds remaining          ", bar, mins, secs)

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

// ─── Crypt of Cashu ────────────────────────────────────────────

type roomKind int

const (
	roomMonster roomKind = iota
	roomTreasure
	roomEmpty
	roomFountain
	roomTrap
	roomShop
	roomBoss
)

type room struct {
	kind     roomKind
	monster  string
	resolved string
	visited  bool
}

type player struct {
	hp      int
	maxHP   int
	gold    int
	sword   bool
	shield  bool
	potions int
	level   int
}

var monsters = []string{
	"Satoshi Goblin", "Lightning Elemental", "Mint Golem", "Fee Bandit",
	"Confirmation Banshee", "RBF Wraith", "UTXO Spawn", "Dust Demon",
	"HODL Revenant", "Pleb Eater",
}

var bossMonsters = []string{
	"The Chain Splitter", "Lord Blocksize", "The Fiat Dragon",
}

var emptyFlavors = []string{
	"The chamber is empty. Cryptographic dust drifts in dim light.",
	"Collapsed mining rigs line the walls. Nothing of value.",
	"An empty block template. You move through quickly.",
	"Silence. Only echoes of past transactions remain.",
	"A cold draft. Empty mempool. You press onward.",
}

var trapFlavors = []string{
	"A hidden fee trap! Deducted 1 HP.",
	"A replay attack! You narrowly escape but lose 1 HP.",
	"A stale block falls from the ceiling! -1 HP.",
}

func cryptGame(s *session) {
	currentMode = "crypt"
	defer drainKeys()

	if s.amount < 1 {
		fmt.Println("\n  Not enough satoshis for an adventure!")
		readLine()
		return
	}

	p := player{
		hp:    s.amount,
		maxHP: s.amount,
		level: 1,
	}

	totalLevels := 3
	roomsPerLevel := s.amount
	if roomsPerLevel > 12 {
		roomsPerLevel = 12
	}
	if roomsPerLevel < 5 {
		roomsPerLevel = 5
	}

	for p.level <= totalLevels {
		if s.expired() {
			return
		}

		rooms := make([]room, roomsPerLevel)
		for i := range rooms {
			if i == roomsPerLevel-1 {
				rooms[i] = room{kind: roomBoss, monster: bossMonsters[(p.level-1)%len(bossMonsters)]}
				continue
			}
			roll := rand.Intn(100)
			switch {
			case roll < 40:
				rooms[i] = room{kind: roomMonster, monster: monsters[rand.Intn(len(monsters))]}
			case roll < 60:
				rooms[i] = room{kind: roomTreasure}
			case roll < 70:
				rooms[i] = room{kind: roomFountain}
			case roll < 80:
				rooms[i] = room{kind: roomTrap}
			case roll < 88:
				rooms[i] = room{kind: roomShop}
			default:
				rooms[i] = room{kind: roomEmpty}
			}
		}

		currentRoom := 0
		enterRoom(&rooms[currentRoom], &p)
		showCryptStatus(&p, &rooms[currentRoom])

		for {
			if p.hp <= 0 {
				fmt.Println("\n  You died! The crypt claims another soul.")
				fmt.Println("\n  [Press Enter to return to arcade]")
				readLine()
				return
			}
			if s.expired() {
				return
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
					fmt.Printf("\n  "+bold+"Level %d cleared!"+reset+"\n", p.level)
					p.level++
					if p.level > totalLevels {
						fmt.Println("\n  *** VICTORY! You conquered the Crypt of Cashu! ***")
						fmt.Printf("  Final: HP %d/%d, Gold %d\n", p.hp, p.maxHP, p.gold)
						fmt.Println("\n  [Press Enter to return to arcade]")
						readLine()
						return
					}
					fmt.Printf("  Descending to level %d...\n", p.level)
					fmt.Println("  [Press Enter to continue]")
					readLine()
					goto nextLevel
				}
				enterRoom(&rooms[currentRoom], &p)
				showCryptStatus(&p, &rooms[currentRoom])

			case cmd == "look" || cmd == "l":
				fmt.Printf("  %s\n", rooms[currentRoom].resolved)

			case cmd == "status" || cmd == "s":
				showCryptStatus(&p, &rooms[currentRoom])

			case cmd == "drink" || cmd == "d":
				if p.potions > 0 && p.hp < p.maxHP {
					p.potions--
					heal := 2
					if p.hp+heal > p.maxHP {
						heal = p.maxHP - p.hp
					}
					p.hp += heal
					fmt.Printf("  You drink a potion. +%d HP. (HP %d/%d)\n", heal, p.hp, p.maxHP)
				} else if p.potions == 0 {
					fmt.Println("  No potions left!")
				} else {
					fmt.Println("  Already at full HP!")
				}

			case cmd == "help" || cmd == "h":
				fmt.Println("  Commands: look, advance, status, drink, help, flee")

			case cmd == "flee" || cmd == "q":
				fmt.Println("  You flee the crypt!")
				return

			default:
				fmt.Println("  Commands: look, advance, status, drink, help, flee")
			}
		}

	nextLevel:
	}
}

func showCryptStatus(p *player, r *room) {
	items := []string{}
	if p.sword {
		items = append(items, "Sword")
	}
	if p.shield {
		items = append(items, "Shield")
	}
	if p.potions > 0 {
		items = append(items, fmt.Sprintf("Potions\u00d7%d", p.potions))
	}
	itemStr := strings.Join(items, ", ")
	if itemStr == "" {
		itemStr = "none"
	}
	fmt.Printf("  "+bold+"HP %d/%d"+reset+" | Gold %d | Lv %d | Items: %s\n",
		p.hp, p.maxHP, p.gold, p.level, itemStr)
	if r.kind == roomBoss {
		fmt.Printf("  "+bold+"\u26a0 BOSS ROOM"+reset+" \u2014 %s\n", r.monster)
	}
	fmt.Printf("  %s\n", r.resolved)
}

func enterRoom(r *room, p *player) {
	r.visited = true
	switch r.kind {
	case roomMonster:
		pRoll := rand.Intn(6) + 1
		if p.sword {
			pRoll++
		}
		mRoll := rand.Intn(6) + 1
		if p.shield {
			mRoll--
		}
		if mRoll >= pRoll {
			p.hp--
			r.resolved = fmt.Sprintf("A %s strikes! You roll %d, it rolls %d. -1 HP!", r.monster, pRoll, mRoll)
		} else {
			goldDrop := rand.Intn(3) + 1
			p.gold += goldDrop
			r.resolved = fmt.Sprintf("A %s attacks! Roll %d vs %d. You win! +%d gold.", r.monster, pRoll, mRoll, goldDrop)
		}
	case roomBoss:
		pRoll := rand.Intn(8) + 2
		if p.sword {
			pRoll += 2
		}
		mRoll := rand.Intn(8) + 2
		if p.shield {
			mRoll--
		}
		if mRoll >= pRoll {
			p.hp -= 2
			r.resolved = fmt.Sprintf("%s unleashes fury! Roll %d vs %d. -2 HP!", r.monster, pRoll, mRoll)
		} else {
			p.gold += 10
			p.maxHP++
			p.hp++
			r.resolved = fmt.Sprintf("You defeat %s! Roll %d vs %d. +10 gold, +1 max HP!", r.monster, pRoll, mRoll)
		}
	case roomTreasure:
		gold := rand.Intn(5) + 2
		p.gold += gold
		roll := rand.Intn(10)
		switch {
		case roll < 2 && !p.sword:
			p.sword = true
			r.resolved = fmt.Sprintf("A glittering cache! You find %d gold and a Sword! (+1 atk)", gold)
		case roll < 4 && !p.shield:
			p.shield = true
			r.resolved = fmt.Sprintf("A hoard of satoshis! %d gold and a Shield! (-1 foe)", gold)
		case roll < 6:
			p.potions++
			r.resolved = fmt.Sprintf("A hidden stash! %d gold and a Potion! (+2 HP)", gold)
		default:
			r.resolved = fmt.Sprintf("Glittering satoshis! +%d gold.", gold)
		}
	case roomFountain:
		heal := 2
		if p.hp+heal > p.maxHP {
			heal = p.maxHP - p.hp
		}
		p.hp += heal
		r.resolved = fmt.Sprintf("A healing fountain restores %d HP. (HP %d/%d)", heal, p.hp, p.maxHP)
	case roomTrap:
		p.hp--
		r.resolved = trapFlavors[rand.Intn(len(trapFlavors))]
	case roomShop:
		r.resolved = "A mysterious merchant offers: [b]uy potion (5 gold), [h]eal (3 gold), [l]eave"
		fmt.Printf("  %s\n", r.resolved)
		shopLoop(p)
		r.resolved = "The merchant vanishes. The room is empty now."
	case roomEmpty:
		r.resolved = emptyFlavors[rand.Intn(len(emptyFlavors))]
	}
}

func shopLoop(p *player) {
	for {
		fmt.Print("  Shop> ")
		cmd, ok := readLine()
		if !ok {
			return
		}
		cmd = strings.ToLower(cmd)
		switch cmd {
		case "b", "buy":
			if p.gold >= 5 {
				p.gold -= 5
				p.potions++
				fmt.Println("  Bought a potion! (+2 HP when used)")
			} else {
				fmt.Println("  Not enough gold! (need 5)")
			}
		case "h", "heal":
			if p.gold >= 3 && p.hp < p.maxHP {
				p.gold -= 3
				heal := 3
				if p.hp+heal > p.maxHP {
					heal = p.maxHP - p.hp
				}
				p.hp += heal
				fmt.Printf("  Healed %d HP. (HP %d/%d)\n", heal, p.hp, p.maxHP)
			} else if p.gold < 3 {
				fmt.Println("  Not enough gold! (need 3)")
			} else {
				fmt.Println("  Already at full HP!")
			}
		case "l", "leave", "q":
			return
		default:
			fmt.Println("  [b]uy potion (5g) | [h]eal (3g) | [l]eave")
		}
	}
}

// ─── Snake ─────────────────────────────────────────────────────

type point struct{ x, y int }

const (
	snakeW = 50
	snakeH = 19
)

func snakeGame(s *session) {
	currentMode = "snake"
	defer func() {
		currentMode = "arcade"
		drainKeys()
	}()

	fullScreen()
	fmt.Print(hideCursor)

	snake := []point{{25, 10}, {24, 10}, {23, 10}}
	dir := point{1, 0}
	food := randomFood(snake)
	score := 0

	renderSnake(snake, food, score, s)

	tickMs := 150
	ticker := time.NewTicker(time.Duration(tickMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		if s.expired() {
			return
		}

		select {
		case <-ticker.C:
			head := point{snake[0].x + dir.x, snake[0].y + dir.y}

			if head.x < 1 || head.x > snakeW || head.y < 1 || head.y > snakeH {
				snakeGameOver(score)
				return
			}
			for _, seg := range snake {
				if seg == head {
					snakeGameOver(score)
					return
				}
			}

			snake = append([]point{head}, snake...)

			if head == food {
				score++
				food = randomFood(snake)
				if score%5 == 0 && tickMs > 80 {
					tickMs -= 10
					ticker.Reset(time.Duration(tickMs) * time.Millisecond)
				}
			} else {
				snake = snake[:len(snake)-1]
			}

			renderSnake(snake, food, score, s)

		case b := <-keyChan:
			newDir := dir
			if b == 0x1b {
				select {
				case b2 := <-keyChan:
					if b2 == '[' {
						b3 := <-keyChan
						switch b3 {
						case 'A':
							newDir = point{0, -1}
						case 'B':
							newDir = point{0, 1}
						case 'C':
							newDir = point{1, 0}
						case 'D':
							newDir = point{-1, 0}
						}
					}
				default:
					snakeGameOver(score)
					return
				}
			} else {
				switch b {
				case 'w', 'W':
					newDir = point{0, -1}
				case 's', 'S':
					newDir = point{0, 1}
				case 'd', 'D':
					newDir = point{1, 0}
				case 'a', 'A':
					newDir = point{-1, 0}
				case 'q', 'Q', 3:
					snakeGameOver(score)
					return
				}
			}
			if newDir.x != -dir.x || newDir.y != -dir.y {
				dir = newDir
			}
		}
	}
}

func randomFood(snake []point) point {
	for {
		p := point{rand.Intn(snakeW) + 1, rand.Intn(snakeH) + 1}
		onSnake := false
		for _, seg := range snake {
			if seg == p {
				onSnake = true
				break
			}
		}
		if !onSnake {
			return p
		}
	}
}

func renderSnake(snake []point, food point, score int, s *session) {
	fmt.Print(cursorHome)
	fmt.Print(clrFromDown)

	// draw border
	for x := 0; x <= snakeW+1; x++ {
		move(1, x+15)
		fmt.Print("\u2550")
		move(snakeH+2, x+15)
		fmt.Print("\u2550")
	}
	for y := 1; y <= snakeH+1; y++ {
		move(y, 15)
		fmt.Print("\u2551")
		move(y, snakeW+16)
		fmt.Print("\u2551")
	}
	move(1, 15)
	fmt.Print("\u2554")
	move(1, snakeW+16)
	fmt.Print("\u2557")
	move(snakeH+2, 15)
	fmt.Print("\u255a")
	move(snakeH+2, snakeW+16)
	fmt.Print("\u255d")

	// draw snake
	for i, seg := range snake {
		move(seg.y+2, seg.x+15)
		if i == 0 {
			fmt.Print(bold + "@" + reset)
		} else {
			fmt.Print("o")
		}
	}

	// draw food
	move(food.y+2, food.x+15)
	fmt.Print("\033[33m*" + reset)

	// draw score and instructions
	move(snakeH+4, 2)
	fmt.Printf("  Score: %d  ", score)
	move(snakeH+4, 25)
	fmt.Print(dim + "Arrow keys / WASD to move \u2014 ESC or Q to quit" + reset)

	// status bar
	statusBarLine(s)
}

func snakeGameOver(score int) {
	fmt.Print(showCursor)
	fullScreen()
	fmt.Print(clrScreen, cursorHome)
	fmt.Println()
	fmt.Printf("  " + bold + "SNAKE \u2014 GAME OVER" + reset + "\n\n")
	fmt.Printf("  Score: %d\n\n", score)
	fmt.Println("  [Press Enter to return to arcade]")
	readLine()
}

// ─── Main ───────────────────────────────────────────────────────

func main() {
	s := loadSession()

	if s.amount <= 0 || s.duration <= 0 {
		fmt.Println("Invalid session. Missing TOLLGATE environment variables.")
		os.Exit(1)
	}

	rand.Seed(time.Now().UnixNano())

	startExpiryWatcher(s)
	showBanner(s)

	// main arcade loop
	for {
		if s.expired() {
			return
		}

		choice := arcadePicker(s)
		switch choice {
		case "crypt":
			currentMode = "crypt"
			setScrollRegion()
			sbStop := make(chan struct{})
			startStatusBar(s, sbStop)
			cryptGame(s)
			close(sbStop)
		case "snake":
			currentMode = "snake"
			snakeGame(s)
		case "timer":
			currentMode = "timer"
			setScrollRegion()
			sbStop := make(chan struct{})
			startStatusBar(s, sbStop)
			timerMode(s)
			close(sbStop)
		case "quit":
			fullScreen()
			fmt.Print(showCursor)
			fmt.Print(clrScreen, cursorHome)
			fmt.Println()
			fmt.Println("  " + bold + "Thanks for playing at tollgate-auth!" + reset)
			fmt.Println("  Ecash for infrastructure access \u2014 cashu.space")
			fmt.Println()
			return
		}
	}
}
