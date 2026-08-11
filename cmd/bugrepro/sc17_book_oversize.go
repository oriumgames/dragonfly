package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

func init() {
	register(Scenario{
		ID:      "17-book-oversized-title",
		Title:   "A signed book accepts a megabyte of title and author",
		Timeout: 180 * time.Second,
		Bug: "`BookEditHandler.Handle` bounds the page text but nothing else:\n\n" +
			"```go\n" +
			"if len(pk.Text) > 256 {\n    return fmt.Errorf(\"text can not be longer than 256 bytes\")\n}\n" +
			"...\n" +
			"case packet.BookActionSign:\n" +
			"    _ = s.inv.SetItem(slot, it.WithItem(item.WrittenBook{Title: pk.Title, Author: pk.Author, ...}))\n" +
			"```\n\n" +
			"`pk.Title` and `pk.Author` are taken straight from the client with no length check at all, so a\n" +
			"client can put arbitrarily large strings in an item that then lives in the world, is written to\n" +
			"the level database and is broadcast to every other client that sees it.",
		Run:   runBookOversize,
		Child: true,
		OnChildCrash: func(partial, stderr string, err error) Result {
			return Result{
				Verdict: Reproduced,
				Setup:   "A book and quill in hotbar slot 0; the client sends a `packet.BookEdit` sign action with a megabyte of title and author.",
				ServerSteps: []string{
					"gave the player a book and quill and read the resulting item back",
					"run as a child process because the server may not survive the packet",
				},
				ClientSteps: []string{"a real `packet.BookEdit` with `ActionType: BookActionSign` and 1 MiB strings in Title and Author"},
				Observed:    partial + "\n--- child process output ---\n" + truncate(stderr, 4000) + fmt.Sprintf("\n--- child exit: %v ---", err),
				Expected: "The server should reject or truncate an oversized title/author the same way it rejects\n" +
					"page text over 256 bytes (vanilla caps the title at 16 characters). It certainly should not\n" +
					"crash.",
				Reason: "`BookEditHandler.Handle` stores the client's `Title` and `Author` verbatim. `s.inv.SetItem`\n" +
					"then fires the slot-change callback, which writes an `InventorySlot` packet back to the\n" +
					"client. gophertunnel's NBT writer refuses to encode a string longer than 32767 bytes and\n" +
					"**panics** rather than returning an error, on the session's packet-writing goroutine:\n\n" +
					"```\npanic: nbt: string at offset 10 is not valid: string length exceeds maximum length (len=1048576)\n" +
					"    protocol.(*Writer).NBT -> itemUserData -> ItemInstance -> packet.(*InventorySlot).Marshal\n" +
					"    -> minecraft.(*Conn).WritePacket -> session.Config.New.func1\n```\n\n" +
					"The whole server process goes down. Any client can send this packet.",
				Summary: "the whole server process panics (nbt string length exceeds maximum) when a client signs a book with a 1 MiB title",
			}
		},
	})
}

func runBookOversize() Result {
	h, err := startHarness(harnessOpts{withClient: true, randomTickSpeed: -1})
	if err != nil {
		return Result{Verdict: Blocked, Reason: "could not start harness: " + err.Error()}
	}
	defer h.Stop()

	var o out
	res := Result{
		Setup: "A book and quill in the player's hotbar slot 0. The client sends a real `packet.BookEdit`\n" +
			"with `ActionType: BookActionSign`, a title of 1 MiB of 'A' and an author of 1 MiB of 'B'.",
		ServerSteps: []string{
			"gave the player a `item.BookAndQuill` in slot 0",
			"read the resulting `item.WrittenBook` back out of the inventory and measured the stored string lengths",
			"re-encoded the resulting stack through `item.WriteNBT` to measure how big the saved item becomes",
		},
		ClientSteps: []string{
			"a real `packet.BookEdit` with `ActionType: packet.BookActionSign` carrying 1 MiB in `Title` and 1 MiB in `Author`",
		},
	}

	const size = 1 << 20 // 1 MiB
	title := strings.Repeat("A", size)
	author := strings.Repeat("B", size)

	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		preparePlayer(tx, p, mgl64.Vec3{160.5, -60, 160.5})
		_ = p.Inventory().SetItem(0, item.NewStack(item.BookAndQuill{}, 1))
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}
	time.Sleep(600 * time.Millisecond)
	o.printf("player holds a book and quill in slot 0")
	o.printf("client sends BookEdit{ActionType: Sign} with len(Title)=%d and len(Author)=%d", len(title), len(author))

	if err := h.Send(&packet.BookEdit{
		ActionType:    packet.BookActionSign,
		InventorySlot: 0,
		Title:         title,
		Author:        author,
		XUID:          "1234567890",
	}); err != nil {
		return blocked(res, "sending BookEdit failed: "+err.Error())
	}
	time.Sleep(2500 * time.Millisecond)

	var (
		titleLen, authorLen int
		isWritten           bool
		nbtSize             int
	)
	if err := h.Do(func(tx *world.Tx, p *player.Player) {
		s, _ := p.Inventory().Item(0)
		if b, ok := s.Item().(item.WrittenBook); ok {
			isWritten = true
			titleLen, authorLen = len(b.Title), len(b.Author)
		}
		nbtSize = nbtByteSize(s)
	}); err != nil {
		return blocked(res, "world call failed: "+err.Error())
	}

	o.printf("")
	o.printf("server-side item in slot 0 is a written book : %v", isWritten)
	o.printf("stored Title length                          : %d bytes", titleLen)
	o.printf("stored Author length                         : %d bytes", authorLen)
	o.printf("size of the stack once encoded for disk      : %d bytes", nbtSize)

	res.Observed = o.String()
	res.Expected = "The sign action should validate the title and author lengths, the way the page text is\n" +
		"validated at 256 bytes. Vanilla caps the title at 16 characters. The stored lengths should be\n" +
		"small, not 1048576."

	if isWritten && titleLen == size && authorLen == size {
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("the server stored a %d byte title and a %d byte author with no validation (the encoded stack is %d bytes)",
			titleLen, authorLen, nbtSize)
	} else if isWritten && (titleLen > 256 || authorLen > 256) {
		res.Verdict = Reproduced
		res.Summary = fmt.Sprintf("the server stored a %d byte title and %d byte author (page text is capped at 256)", titleLen, authorLen)
	} else {
		res.Verdict = NotReproduced
		res.Summary = fmt.Sprintf("written=%v title=%d author=%d", isWritten, titleLen, authorLen)
	}
	return res
}

// nbtByteSize measures the size of a stack once encoded the way the level
// database stores it.
func nbtByteSize(s item.Stack) int {
	if s.Empty() {
		return 0
	}
	data := item.WriteNBT(s, true)
	return roughSize(data)
}

// roughSize walks an NBT map and adds up the size of its string values and keys.
func roughSize(m map[string]any) int {
	n := 0
	for k, v := range m {
		n += len(k)
		switch v := v.(type) {
		case string:
			n += len(v)
		case map[string]any:
			n += roughSize(v)
		case []any:
			for _, e := range v {
				if s, ok := e.(string); ok {
					n += len(s)
				} else if mm, ok := e.(map[string]any); ok {
					n += roughSize(mm)
				}
			}
		default:
			n += 8
		}
	}
	return n
}
