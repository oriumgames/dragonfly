package main

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/df-mc/dragonfly/server/player"
	"github.com/df-mc/dragonfly/server/player/skin"
	"github.com/df-mc/dragonfly/server/session"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol/login"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// loopbackConn is a real session.Conn that carries packets in memory instead
// of over a socket. Everything above it - the Session, its packet handlers,
// its chunk loader and its inventories - is the real thing.
type loopbackConn struct {
	id  login.IdentityData
	cd  login.ClientData
	in  chan packet.Packet
	mu  sync.Mutex
	out []packet.Packet

	closeOnce sync.Once
	closed    chan struct{}
}

func newLoopbackConn(name string, id uuid.UUID) *loopbackConn {
	return &loopbackConn{
		id:     login.IdentityData{DisplayName: name, Identity: id.String(), XUID: "0"},
		in:     make(chan packet.Packet, 64),
		closed: make(chan struct{}),
	}
}

func (c *loopbackConn) IdentityData() login.IdentityData { return c.id }
func (c *loopbackConn) ClientData() login.ClientData     { return c.cd }
func (c *loopbackConn) ClientCacheEnabled() bool         { return false }
func (c *loopbackConn) ChunkRadius() int                 { return 4 }
func (c *loopbackConn) Latency() time.Duration           { return 0 }
func (c *loopbackConn) Flush() error                     { return nil }
func (c *loopbackConn) RemoteAddr() net.Addr             { return dummyAddr{} }

func (c *loopbackConn) StartGameContext(context.Context, minecraft.GameData) error { return nil }

func (c *loopbackConn) ReadPacket() (packet.Packet, error) {
	select {
	case pk := <-c.in:
		return pk, nil
	case <-c.closed:
		return nil, errors.New("loopback conn closed")
	}
}

func (c *loopbackConn) WritePacket(pk packet.Packet) error {
	c.mu.Lock()
	c.out = append(c.out, pk)
	c.mu.Unlock()
	return nil
}

func (c *loopbackConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

// send queues a packet for the Session's own packet handling goroutine.
func (c *loopbackConn) send(pk packet.Packet) {
	select {
	case c.in <- pk:
	case <-c.closed:
	}
}

func (c *loopbackConn) written() []packet.Packet {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]packet.Packet(nil), c.out...)
}

type dummyAddr struct{}

func (dummyAddr) Network() string { return "loopback" }
func (dummyAddr) String() string  { return "loopback:0" }

// sessionPlayer is a real *session.Session driving a real *player.Player in a
// real world, exactly the way server.createPlayer wires them up.
type sessionPlayer struct {
	s      *session.Session
	conn   *loopbackConn
	handle *world.EntityHandle
	id     uuid.UUID
}

func spawnSessionPlayer(o *Out, w *world.World, name string, pos mgl64.Vec3) (*sessionPlayer, error) {
	id := uuid.New()
	conn := newLoopbackConn(name, id)
	s := session.Config{
		Log:            captureLoggerDebug(o, "session:"+name),
		MaxChunkRadius: 4,
		HandleStop:     func(*world.Tx, session.Controllable) {},
		BlockRegistry:  w.BlockRegistry(),
	}.New(conn)

	conf := player.Config{Session: s, Name: name, UUID: id, Position: pos}
	handle := world.EntitySpawnOpts{Position: pos, ID: id}.New(player.Type, conf)
	s.SetHandle(handle, skin.New(64, 32))

	if err := call(w, 15*time.Second, func(tx *world.Tx) {
		p := tx.AddEntity(handle).(*player.Player)
		s.Spawn(p, tx)
	}); err != nil {
		return nil, err
	}
	return &sessionPlayer{s: s, conn: conn, handle: handle, id: id}, nil
}

// player runs f with the real *player.Player on its world owner.
func (sp *sessionPlayer) player(w *world.World, d time.Duration, f func(tx *world.Tx, p *player.Player)) error {
	return call(w, d, func(tx *world.Tx) {
		e, ok := sp.handle.Entity(tx)
		if !ok {
			return
		}
		f(tx, e.(*player.Player))
	})
}
