package session

import (
	"fmt"
	"math"
	"time"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/entity"
	"github.com/df-mc/dragonfly/server/entity/effect"
	"github.com/df-mc/dragonfly/server/internal/nbtconv"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/item/potion"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/google/uuid"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// parseEntityMetadata returns an entity metadata object with default values. It is equivalent to setting
// all properties to their default values and disabling all flags.
func (s *Session) parseEntityMetadata(e world.Entity) protocol.EntityMetadata {
	bb := e.H().Type().BBox(e)
	m := protocol.NewEntityMetadata()

	m[protocol.EntityDataKeyWidth] = float32(bb.Width())
	m[protocol.EntityDataKeyHeight] = float32(bb.Height())
	m[protocol.EntityDataKeyEffectColor] = int32(0)
	m[protocol.EntityDataKeyEffectAmbience] = byte(0)
	m[protocol.EntityDataKeyColorIndex] = byte(0)

	if g, ok := e.(gravity); !ok || g.HasGravity() {
		setEntityFlag(m, protocol.EntityDataFlagHasGravity)
	}
	if c, ok := e.(climber); !ok || c.Climb() {
		setEntityFlag(m, protocol.EntityDataFlagClimb)
	}
	if g, ok := e.H().Type().(glint); ok && g.Glint() {
		m.SetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagEnchanted)
	}
	if e.H().Type() == entity.LingeringPotionType {
		m.SetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagLingering)
	}
	s.addSpecificMetadata(e, m)
	if ent, ok := e.(*entity.Ent); ok {
		s.addSpecificMetadata(ent.Behaviour(), m)
	}
	return m
}

func (s *Session) addSpecificMetadata(e any, m protocol.EntityMetadata) {
	if sn, ok := e.(sneaker); ok && sn.Sneaking() {
		m.SetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagSneaking)
	}
	if sp, ok := e.(sprinter); ok && sp.Sprinting() {
		m.SetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagSprinting)
	}
	if sw, ok := e.(swimmer); ok && sw.Swimming() {
		m.SetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagSwimming)
	}
	if cr, ok := e.(crawler); ok && cr.Crawling() {
		setEntityFlag(m, protocol.EntityDataFlagCrawling)
	}
	if gl, ok := e.(glider); ok && gl.Gliding() {
		m.SetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagGliding)
	}
	if bb, ok := e.(baby); ok && bb.Baby() {
		m.SetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagBaby)
	}
	if b, ok := e.(breather); ok {
		m[protocol.EntityDataKeyAirSupply] = int16(b.AirSupply().Milliseconds() / 50)
		m[protocol.EntityDataKeyAirSupplyMax] = int16(b.MaxAirSupply().Milliseconds() / 50)
		if b.Breathing() {
			m.SetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagBreathing)
		}
	}
	if i, ok := e.(invisible); ok && i.Invisible() {
		m.SetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagInvisible)
	}
	if i, ok := e.(immobile); ok && i.Immobile() {
		m.SetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagNoAI)
	}
	if o, ok := e.(onFire); ok && o.OnFireDuration() > 0 {
		m.SetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagOnFire)
	}
	if u, ok := e.(using); ok && u.UsingItem() {
		m.SetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagUsingItem)
	}
	if c, ok := e.(arrow); ok && c.Critical() {
		m.SetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagCritical)
	}
	if g, ok := e.(gameMode); ok {
		if g.GameMode().HasCollision() {
			m.SetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagHasCollision)
		}
	}
	if o, ok := e.(orb); ok {
		m[protocol.EntityDataKeyValue] = int32(o.Experience())
	}
	if f, ok := e.(firework); ok {
		m[protocol.EntityDataKeyDisplayFirework] = item.WriteNBT(item.NewStack(f.Firework(), 1), false)
		if o, ok := e.(owned); ok && f.Attached() && o.Owner() != nil {
			m[protocol.EntityDataKeyCustomDisplay] = int64(s.handleRuntimeID(o.Owner()))
		}
	} else if o, ok := e.(owned); ok && o.Owner() != nil {
		m[protocol.EntityDataKeyOwner] = int64(s.handleRuntimeID(o.Owner()))
	}
	if sc, ok := e.(scaled); ok {
		m[protocol.EntityDataKeyScale] = float32(sc.Scale())
	}
	if t, ok := e.(tnt); ok {
		m[protocol.EntityDataKeyFuseTime] = int32(t.Fuse().Milliseconds() / 50)
		m.SetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagIgnited)
	}
	if nameTag, alwaysShow, ok := nameTagState(e); ok {
		writeNameTagMetadata(m, nameTag, alwaysShow)
	}
	if sc, ok := e.(scoreTag); ok {
		m[protocol.EntityDataKeyScore] = sc.ScoreTag()
	}
	if c, ok := e.(endCrystal); ok {
		if c.ShowBase() {
			m.SetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagShowBottom)
		} else {
			m.UnsetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagShowBottom)
		}
		if target, ok := c.BeamTarget(); ok {
			m[protocol.EntityDataKeyBlockTarget] = protocol.BlockPos{int32(target[0]), int32(target[1]), int32(target[2])}
		}
	}
	if sl, ok := e.(sleeper); ok {
		if pos, ok := sl.Sleeping(); ok {
			m[protocol.EntityDataKeyBedPosition] = protocol.BlockPos{int32(pos[0]), int32(pos[1]), int32(pos[2])}

			// For some reason there is no such flag in gophertunnel.
			m.SetFlag(protocol.EntityDataKeyPlayerFlags, 1)
		}
	}
	if c, ok := e.(areaEffectCloud); ok {
		m[protocol.EntityDataKeyDataRadius] = float32(c.Radius())

		// We purposely fill these in with invalid values to disable the client-sided shrinking of the cloud.
		m[protocol.EntityDataKeyDataDuration] = int32(math.MaxInt32)
		m[protocol.EntityDataKeyDataChangeOnPickup] = float32(math.SmallestNonzeroFloat32)
		m[protocol.EntityDataKeyDataChangeRate] = float32(math.SmallestNonzeroFloat32)

		colour, am := effect.ResultingColour(c.Effects())
		m[protocol.EntityDataKeyEffectColor] = nbtconv.Int32FromRGBA(colour)
		if am {
			m[protocol.EntityDataKeyEffectAmbience] = byte(1)
		} else {
			m[protocol.EntityDataKeyEffectAmbience] = byte(0)
		}
	}

	if l, ok := e.(living); ok && s.ent != nil && s.ent.UUID() == l.UUID() {
		deathPos, deathDimension, died := l.DeathPosition()
		if died {
			dim, _ := world.DimensionID(deathDimension)
			m[protocol.EntityDataKeyPlayerLastDeathPosition] = vec64To32(deathPos)
			m[protocol.EntityDataKeyPlayerLastDeathDimension] = int32(dim)
		}
		m[protocol.EntityDataKeyPlayerHasDied] = boolByte(died)
	}
	if p, ok := e.(splash); ok {
		m[protocol.EntityDataKeyAuxValueData] = int16(p.Potion().Uint8())
		if tip := p.Potion().Uint8(); tip > 4 {
			m[protocol.EntityDataKeyCustomDisplay] = tip + 1
		}
	}
	if eff, ok := e.(effectBearer); ok {
		var packedEffects int64

		for _, ef := range eff.Effects() {
			if !ef.ParticlesHidden() {
				id, found := effect.ID(ef.Type())
				if !found {
					continue
				}
				packedEffects = (packedEffects << 7) | int64(id<<1)
				if ef.Ambient() {
					packedEffects |= 1
				}
			}
		}
		m[protocol.EntityDataKeyVisibleMobEffects] = packedEffects
	}
	if v, ok := e.(variable); ok {
		m[protocol.EntityDataKeyVariant] = v.Variant()
	}
	if mv, ok := e.(markVariable); ok {
		m[protocol.EntityDataKeyMarkVariant] = mv.MarkVariant()
	}
	if a, ok := e.(angry); ok && a.Angry() {
		setEntityFlag(m, protocol.EntityDataFlagAngry)
	}
	if si, ok := e.(sitter); ok && si.Sitting() {
		setEntityFlag(m, protocol.EntityDataFlagSitting)
	}
	if ta, ok := e.(tamed); ok && ta.Tamed() {
		setEntityFlag(m, protocol.EntityDataFlagTamed)
	}
	if sa, ok := e.(saddled); ok && sa.Saddled() {
		setEntityFlag(m, protocol.EntityDataFlagSaddled)
	}
	if ch, ok := e.(charged); ok && ch.Charged() {
		setEntityFlag(m, protocol.EntityDataFlagCharged)
	}
	if st, ok := e.(stander); ok && st.Standing() {
		setEntityFlag(m, protocol.EntityDataFlagStanding)
	}
	if il, ok := e.(lover); ok && il.InLove() {
		setEntityFlag(m, protocol.EntityDataFlagInLove)
	}
	if sh, ok := e.(sheared); ok && sh.Sheared() {
		setEntityFlag(m, protocol.EntityDataFlagSheared)
	}
	if ce, ok := e.(chested); ok && ce.Chested() {
		setEntityFlag(m, protocol.EntityDataFlagChested)
	}
	if su, ok := e.(stunned); ok && su.Stunned() {
		setEntityFlag(m, protocol.EntityDataFlagStunned)
	}
	if ro, ok := e.(roarer); ok && ro.Roaring() {
		setEntityFlag(m, protocol.EntityDataFlagRoaring)
	}
	if cb, ok := e.(celebrator); ok && cb.Celebrating() {
		setEntityFlag(m, protocol.EntityDataFlagCelebrating)
	}
}

// entityFlagWordSize is the number of flags held by each of the two words that entity flags are stored in.
const entityFlagWordSize = 64

// setEntityFlag sets the entity flag with the index passed. Flags are stored across two words, and the index of a flag
// in the second of them is relative to the first, so it has to be brought back into range before it is set.
func setEntityFlag(m protocol.EntityMetadata, index uint8) {
	if index >= entityFlagWordSize*2 {
		// Only two words of flags exist. protocol.EntityMetadata.SetFlag shifts an int64 by the index, which Go
		// evaluates to 0 for a shift of 64 or more, so a flag beyond the second word would silently set nothing.
		panic(fmt.Sprintf("entity flag %v is out of range: only %v flags can be represented", index, entityFlagWordSize*2))
	}
	if index >= entityFlagWordSize {
		m.SetFlag(protocol.EntityDataKeyFlagsTwo, index-entityFlagWordSize)
		return
	}
	m.SetFlag(protocol.EntityDataKeyFlags, index)
}

// nameTagState returns the public name tag of an entity, whether that name tag is shown at all distances
// and whether the entity has a name tag at all. Entities that do not report an always show state show
// their name tag at all distances.
func nameTagState(e any) (string, bool, bool) {
	alwaysShow := true
	if a, ok := e.(alwaysShowNameTag); ok {
		alwaysShow = a.AlwaysShowNameTag()
	}
	n, ok := e.(named)
	if !ok {
		return "", alwaysShow, false
	}
	return n.NameTag(), alwaysShow, true
}

// writeNameTagMetadata writes a name tag and its related visibility properties to metadata.
func writeNameTagMetadata(m protocol.EntityMetadata, nameTag string, alwaysShow bool) {
	show := nameTag != ""
	always := show && alwaysShow

	m[protocol.EntityDataKeyName] = nameTag
	m[protocol.EntityDataKeyAlwaysShowNameTag] = boolByte(always)
	if show {
		m.SetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagShowName)
	} else {
		m.UnsetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagShowName)
	}
	if always {
		m.SetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagAlwaysShowName)
	} else {
		m.UnsetFlag(protocol.EntityDataKeyFlags, protocol.EntityDataFlagAlwaysShowName)
	}
}

type gravity interface {
	HasGravity() bool
}

type climber interface {
	Climb() bool
}

type angry interface {
	Angry() bool
}

type sitter interface {
	Sitting() bool
}

type tamed interface {
	Tamed() bool
}

type saddled interface {
	Saddled() bool
}

type charged interface {
	Charged() bool
}

type stander interface {
	Standing() bool
}

type lover interface {
	InLove() bool
}

type sheared interface {
	Sheared() bool
}

type chested interface {
	Chested() bool
}

type stunned interface {
	Stunned() bool
}

type roarer interface {
	Roaring() bool
}

type celebrator interface {
	Celebrating() bool
}

type sneaker interface {
	Sneaking() bool
}

type sprinter interface {
	Sprinting() bool
}

type swimmer interface {
	Swimming() bool
}

type crawler interface {
	Crawling() bool
}

type glider interface {
	Gliding() bool
}

type baby interface {
	Baby() bool
}

type breather interface {
	Breathing() bool
	AirSupply() time.Duration
	MaxAirSupply() time.Duration
}

type immobile interface {
	Immobile() bool
}

type invisible interface {
	Invisible() bool
}

type scaled interface {
	Scale() float64
}

type owned interface {
	Owner() *world.EntityHandle
}

type named interface {
	NameTag() string
}

type alwaysShowNameTag interface {
	AlwaysShowNameTag() bool
}

type scoreTag interface {
	ScoreTag() string
}

type endCrystal interface {
	ShowBase() bool
	BeamTarget() (cube.Pos, bool)
}

type splash interface {
	Potion() potion.Potion
}

type glint interface {
	Glint() bool
}

type areaEffectCloud interface {
	effectBearer
	Radius() float64
}

type onFire interface {
	OnFireDuration() time.Duration
}

type effectBearer interface {
	Effects() []effect.Effect
}

type using interface {
	UsingItem() bool
}

type arrow interface {
	Critical() bool
}

type orb interface {
	Experience() int
}

type firework interface {
	Firework() item.Firework
	Attached() bool
}

type gameMode interface {
	GameMode() world.GameMode
}

type sleeper interface {
	Sleeping() (cube.Pos, bool)
}

type tnt interface {
	Fuse() time.Duration
}

type living interface {
	UUID() uuid.UUID
	DeathPosition() (mgl64.Vec3, world.Dimension, bool)
}

type variable interface {
	Variant() int32
}

type markVariable interface {
	MarkVariant() int32
}
