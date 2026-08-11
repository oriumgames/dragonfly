package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/df-mc/dragonfly/server"
	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/session"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/dragonfly/server/world/mcdb"
	"github.com/go-gl/mathgl/mgl32"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Harness holds a running dragonfly server and, optionally, a connected
// gophertunnel client.
type Harness struct {
	Srv      *server.Server
	Addr     string
	Conn     *minecraft.Conn
	PlayerH  *world.EntityHandle
	World    *world.World
	Log      *slog.Logger
	LogBuf   *lineBuffer
	tmpDir   string
	stopOnce sync.Once

	// packets received from the server by the client, collected in the
	// background.
	pmu     sync.Mutex
	packets []packet.Packet
	// stackIDs mirrors the stack network IDs the server told the client about,
	// keyed by window ID and slot. The client needs these to send a valid
	// ItemStackRequest.
	stackIDs map[[2]uint32]int32
	// tick is the client tick counter used for PlayerAuthInput packets.
	tick uint64

	// joined receives the entity handle of every player that joins.
	joined chan *world.EntityHandle
	// extra holds additional clients dialed with AddClient.
	extra []*minecraft.Conn
}

// AddClient dials an extra real gophertunnel client and returns its player
// handle server-side. Used by scenarios that need more than one player.
func (h *Harness) AddClient() (*world.EntityHandle, error) {
	conn, err := minecraft.Dialer{
		ErrorLog: slog.New(slog.NewTextHandler(h.LogBuf, &slog.HandlerOptions{Level: slog.LevelError})),
		IdentityData: login.IdentityData{
			DisplayName: fmt.Sprintf("Bot%d", len(h.extra)+2),
			Identity:    uuid.New().String(),
		},
	}.DialTimeout("raknet", h.Addr, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial extra client: %w", err)
	}
	if err := conn.DoSpawnTimeout(30 * time.Second); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("extra client spawn: %w", err)
	}
	h.extra = append(h.extra, conn)
	go func() {
		for {
			if _, err := conn.ReadPacket(); err != nil {
				return
			}
		}
	}()
	select {
	case ph := <-h.joined:
		return ph, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("timed out waiting for the extra player to join server-side")
	}
}

// DoEntity runs f on the world owner of the entity handle passed.
func (h *Harness) DoEntity(handle *world.EntityHandle, f func(tx *world.Tx, p *player.Player)) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := world.CallEntity(ctx, handle, func(tx *world.Tx, e world.Entity) (struct{}, error) {
		f(tx, e.(*player.Player))
		return struct{}{}, nil
	})
	return err
}

// StackID returns the last stack network ID the server sent the client for the
// given window ID and slot.
func (h *Harness) StackID(windowID, slot uint32) (int32, bool) {
	h.pmu.Lock()
	defer h.pmu.Unlock()
	id, ok := h.stackIDs[[2]uint32{windowID, slot}]
	return id, ok
}

// trackStackIDs updates the mirror from an inventory packet.
func (h *Harness) trackStackIDs(pk packet.Packet) {
	switch p := pk.(type) {
	case *packet.InventoryContent:
		for i, inst := range p.Content {
			h.stackIDs[[2]uint32{p.WindowID, uint32(i)}] = inst.StackNetworkID
		}
	case *packet.InventorySlot:
		h.stackIDs[[2]uint32{p.WindowID, p.Slot}] = p.NewItem.StackNetworkID
	}
}

// harnessOpts configures a Harness.
type harnessOpts struct {
	// withClient dials a real gophertunnel client and waits for it to spawn.
	withClient bool
	// saveWorld uses a real leveldb (mcdb) world provider on disk, which is
	// required for save/reload scenarios.
	saveWorld bool
	// chunkUnloadInterval, if non-zero, sets the interval at which unused
	// chunks are unloaded.
	chunkUnloadInterval time.Duration
	// randomTickSpeed overrides the random tick speed (-1 disables it).
	randomTickSpeed int
	// port forces a specific port instead of a free one.
	port int
}

// freePort returns a UDP port on 127.0.0.1 that is currently unbound.
func freePort() (int, error) {
	a, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	c, err := net.ListenUDP("udp", a)
	if err != nil {
		return 0, err
	}
	p := c.LocalAddr().(*net.UDPAddr).Port
	_ = c.Close()
	return p, nil
}

// startHarness boots a dragonfly server (and optionally a client) and returns
// it. The caller must call Stop.
func startHarness(opts harnessOpts) (*Harness, error) {
	port := opts.port
	if port == 0 {
		var err error
		if port, err = freePort(); err != nil {
			return nil, fmt.Errorf("free port: %w", err)
		}
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	buf := &lineBuffer{}
	var sink io.Writer = buf
	if os.Getenv("BUGREPRO_CHILD") != "" {
		// In a child process the harness may die; mirror the server log to
		// stderr so the parent still sees it.
		sink = io.MultiWriter(buf, os.Stderr)
	}
	log := slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelInfo}))

	h := &Harness{Addr: addr, Log: log, LogBuf: buf, stackIDs: map[[2]uint32]int32{}}

	conf := server.Config{
		Log:                     log,
		Name:                    "bugrepro",
		AuthDisabled:            true,
		DisableResourceBuilding: true,
		MaxChunkRadius:          8,
		RandomTickSpeed:         opts.randomTickSpeed,
		ChunkUnloadInterval:     opts.chunkUnloadInterval,
		Listeners: []func(conf server.Config) (server.Listener, error){
			func(conf server.Config) (server.Listener, error) {
				l, err := minecraft.ListenConfig{
					AuthenticationDisabled: true,
					StatusProvider:         conf.StatusProvider,
					MaximumPlayers:         conf.MaxPlayers,
				}.Listen("raknet", addr)
				if err != nil {
					return nil, err
				}
				return dfListener{l}, nil
			},
		},
	}
	if opts.saveWorld {
		dir, err := os.MkdirTemp("", "bugrepro-world-")
		if err != nil {
			return nil, err
		}
		h.tmpDir = dir
		prov, err := mcdb.Config{Log: log}.Open(dir)
		if err != nil {
			return nil, err
		}
		conf.WorldProvider = prov
	}

	h.Srv = conf.New()
	h.World = h.Srv.World()
	h.Srv.Listen()

	joined := make(chan *world.EntityHandle, 8)
	h.joined = joined
	go func() {
		for p := range h.Srv.Accept() {
			select {
			case joined <- p.H():
			default:
			}
		}
	}()

	if !opts.withClient {
		return h, nil
	}

	conn, err := minecraft.Dialer{
		ErrorLog:            slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelError})),
		KeepXBLIdentityData: false,
	}.DialTimeout("raknet", addr, 30*time.Second)
	if err != nil {
		h.Stop()
		return nil, fmt.Errorf("dial client: %w", err)
	}
	h.Conn = conn
	if err := conn.DoSpawnTimeout(30 * time.Second); err != nil {
		h.Stop()
		return nil, fmt.Errorf("client spawn: %w", err)
	}
	select {
	case ph := <-joined:
		h.PlayerH = ph
	case <-time.After(30 * time.Second):
		h.Stop()
		return nil, fmt.Errorf("timed out waiting for player to join server-side")
	}

	// Drain packets in the background so the connection never stalls.
	go func() {
		for {
			pk, err := conn.ReadPacket()
			if err != nil {
				return
			}
			h.pmu.Lock()
			if len(h.packets) < 20000 {
				h.packets = append(h.packets, pk)
			}
			h.trackStackIDs(pk)
			h.pmu.Unlock()
		}
	}()
	return h, nil
}

// Packets returns a copy of all packets received by the client so far.
func (h *Harness) Packets() []packet.Packet {
	h.pmu.Lock()
	defer h.pmu.Unlock()
	out := make([]packet.Packet, len(h.packets))
	copy(out, h.packets)
	return out
}

// Send writes a packet from the client to the server and flushes it.
func (h *Harness) Send(pk packet.Packet) error {
	if h.Conn == nil {
		return fmt.Errorf("no client connected")
	}
	if err := h.Conn.WritePacket(pk); err != nil {
		return err
	}
	return h.Conn.Flush()
}

// MoveTo sends a real PlayerAuthInput packet reporting the player's feet at
// pos, which is exactly what a Bedrock client does every tick. The server
// converts the delta into a (*Player).Move call.
func (h *Harness) MoveTo(pos mgl64.Vec3) error {
	h.tick++
	return h.Send(&packet.PlayerAuthInput{
		Position:  mgl32.Vec3{float32(pos[0]), float32(pos[1] + 1.62), float32(pos[2])},
		Pitch:     0,
		Yaw:       0,
		HeadYaw:   0,
		InputMode: packet.InputModeMouse,
		PlayMode:  packet.PlayModeNormal,
		Tick:      h.tick,
	})
}

// Do runs f on the player's world owner and waits for it.
func (h *Harness) Do(f func(tx *world.Tx, p *player.Player)) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := world.CallEntity(ctx, h.PlayerH, func(tx *world.Tx, e world.Entity) (struct{}, error) {
		f(tx, e.(*player.Player))
		return struct{}{}, nil
	})
	return err
}

// InWorld runs f on the overworld owner and waits for it.
func (h *Harness) InWorld(f func(tx *world.Tx)) error {
	return inWorld(h.World, f)
}

func inWorld(w *world.World, f func(tx *world.Tx)) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := world.Call(ctx, w, func(tx *world.Tx) (struct{}, error) {
		f(tx)
		return struct{}{}, nil
	})
	return err
}

// Stop shuts the harness down and removes any temporary world directory.
func (h *Harness) Stop() {
	h.stopOnce.Do(func() {
		if h.Conn != nil {
			_ = h.Conn.Close()
		}
		for _, c := range h.extra {
			_ = c.Close()
		}
		if h.Srv != nil {
			_ = h.Srv.Close()
		}
		if h.tmpDir != "" {
			_ = os.RemoveAll(h.tmpDir)
		}
	})
}

// dfListener adapts a *minecraft.Listener to a server.Listener.
type dfListener struct {
	*minecraft.Listener
}

func (l dfListener) Accept() (session.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return conn.(session.Conn), err
}

func (l dfListener) Disconnect(conn session.Conn, reason string) error {
	return l.Listener.Disconnect(conn.(*minecraft.Conn), reason)
}

// lineBuffer is a concurrency safe in-memory log sink.
type lineBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (l *lineBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b = append(l.b, p...)
	return len(p), nil
}

func (l *lineBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.b)
}
