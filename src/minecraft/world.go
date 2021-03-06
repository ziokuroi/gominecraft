package world

import "minecraft/nbt"
import "minecraft/error"

import "fmt"
import "io/ioutil"
import "os"
import "path"

const (
	leveldat    = "level.dat"
	sessionlock = "session.lock"
)

type XZ int64

func MakeXZ(x int32, z int32) XZ {
	return XZ(int64(x) + int64(z)<<32)
}

type World struct {
	dir      string
	lockmsec int64
	// see: http://www.minecraftwiki.net/wiki/Alpha_Level_Format
	Data Data
	// we cheat and use int64, since it has equality defined.
	Chunks map[XZ]*Chunk
	lockfd *os.File
}

type Data struct {
	SnowCovered            int8
	Time                   int64
	SpawnX, SpawnY, SpawnZ int32
	LastPlayed             int64
	SizeOnDisk             int64
	RandomSeed             int64
}

type Chunk struct {
	Level Level
}

type Level struct {
	Blocks           []byte
	Data             []byte
	SkyLight         []byte
	HeightMap        []byte
	BlockLight       []byte
	Entities         []*Entity
	TileEntities     interface{}
	LastUpdate       int64
	XPos             int32
	ZPos             int32
	TerrainPopulated int8
}

type Entity struct {
	Id           string
	OnGround     int8
	Air          int16
	Fire         int16
	Health       *int16
	Tile         *int16
	Item         *Item
	FallDistance float32
	Physics      Physics
	Age          *int16
}

type Item struct {
	Id     int16
	Count  int8
	Damage int16
}

type Physics struct {
	Position Position
	Velocity Velocity
	Euler    Euler
}

type Position struct {
	X, Y, Z float64
}
type Velocity struct {
	DX, DY, DZ float64
}
type Euler struct {
	Yaw, Pitch, Roll float32
}

func Open(worlddir string) (w *World, err os.Error) {
	w = &World{dir: worlddir}
	if err = w.verifyFormat(); err != nil {
		err = error.NewError("could not verify world format", err)
		return
	}
	if err = w.lock(); err != nil {
		err = error.NewError("unable to obtain lock on world", err)
		return
	}
	_, levelDat, err := nbt.Load(path.Join(w.dir, leveldat))
	if err != nil {
		err = error.NewError("could not read level", err)
		return
	}

	w.Chunks = make(map[XZ]*Chunk)
	w.loadLevelDat(levelDat)
	return
}

func (world *World) Close() os.Error {
	return world.unlock()
}

// Flushes any in-memory changes to disk
func (world *World) Flush() os.Error {
	panic("writeme")
}

func (world *World) verifyFormat() (err os.Error) {
	// We don't want to go crazy vetting every byte, but we can at least do a sanity check
	// for how the folder structure should look.  It is important we don't touch any files,
	// so if this world is in use by another process, things don't go terribly wrong.
	fi, err := os.Stat(world.dir)
	if err != nil {
		err = error.NewError("could not stat world directory", err)
		return
	}

	if !fi.IsDirectory() {
		return error.NewError("expected a directory, didn't get one", nil)
	}
	var hasLevelDat, hasSessionLock bool

	files, err := ioutil.ReadDir(world.dir)
	if err != nil {
		err = error.NewError("could not read world directory contents", nil)
		return
	}

	for _, f := range files {
		if f.IsRegular() {
			switch f.Name {
			case leveldat:
				hasLevelDat = true
			case sessionlock:
				hasSessionLock = true
			}
		}
	}

	if !hasLevelDat {
		err = error.NewError(fmt.Sprint("world is missing ", leveldat), nil)
		return
	}
	if !hasSessionLock {
		err = error.NewError(fmt.Sprint("world is missing ", sessionlock), nil)
		return
	}
	return
}

func (world *World) lock() (err os.Error) {
	if world.lockfd != nil {
		panic("lock fd already exists... should never happen")
	}
	sessionLockPath := path.Join(world.dir, sessionlock)
	world.lockfd, err = os.Open(sessionLockPath, os.O_RDWR|os.O_ASYNC, 0000)
	if err != nil {
		error.NewError(fmt.Sprint("could not open ", sessionlock), nil)
	}
	// minecraft's locking mechanism is peculiar.
	// It writes the current system time in milliseconds since 1970 to the file.
	// It then watches the file for changes.  If a change is monitored, it aborts.

	// This has strange implications, such as the LAST process to open the world owns it,
	// not the first.

	// but hey, when in rome...
	sec, nsec, err := os.Time()
	if err != nil {
		err = error.NewError("couldn't get the current time..?!", err)
		return
	}

	world.lockmsec = (sec * 1000) + (nsec / 1000000)
	err = nbt.WriteInt64(world.lockfd, world.lockmsec)
	if err != nil {
		err = error.NewError("could not write timestamp to session lock", err)
		return
	}
	return
}

func (world *World) verifyLock() (err os.Error) {
	_, err = world.lockfd.Seek(0, 0)
	if err != nil {
		err = error.NewError("could not seek to beginning of session lock", err)
		return
	}
	msec, err := nbt.ReadInt64(world.lockfd)
	if err != nil {
		err = error.NewError("could not read timestamp from session lock", err)
		return
	}
	if msec != world.lockmsec {
		err = error.NewError("someone else has opened this world :(", nil)
		return
	}
	return
}

func (world *World) unlock() os.Error {
	return world.lockfd.Close()
}

func (world *World) loadLevelDat(level map[string]interface{}) {
	data := level["Data"].(map[string]interface{})
	world.Data = Data{
		SnowCovered: data["SnowCovered"].(int8),
		Time:        data["Time"].(int64),
		SpawnX:      data["SpawnX"].(int32),
		SpawnY:      data["SpawnY"].(int32),
		SpawnZ:      data["SpawnZ"].(int32),
		LastPlayed:  data["LastPlayed"].(int64),
		SizeOnDisk:  data["SizeOnDisk"].(int64),
		RandomSeed:  data["RandomSeed"].(int64),
	}
}
func posmod64(i int32) int32 {
	if i < 0 {
		i = 64 - i
	}
	return i % 64
}

func (world *World) LoadChunk(x int32, z int32) (err os.Error) {
	if err = world.verifyLock(); err != nil {
		return
	}

	xz := MakeXZ(x, z)
	if _, ok := world.Chunks[xz]; ok {
		return // nothing to do
	}
	var px, pz = posmod64(x), posmod64(z)

	chunkPath := path.Join(
		world.dir,
		int32ToBase36String(px),
		int32ToBase36String(pz),
		fmt.Sprint(
			"c.",
			int32ToBase36String(x),
			".",
			int32ToBase36String(z),
			".dat"))

	_, chunkmap, err := nbt.Load(chunkPath)
	if err != nil {
		err = error.NewError(fmt.Sprintf("could not load chunk (%d, %d)", x, z), err)
		return
	}
	world.Chunks[xz] = toChunk(chunkmap)
	return

}

func toChunk(payload map[string]interface{}) *Chunk {

	levmap := payload["Level"].(map[string]interface{})
	return &Chunk{
		Level: Level{
			Blocks:           levmap["Blocks"].([]byte),
			Data:             levmap["Data"].([]byte),
			SkyLight:         levmap["SkyLight"].([]byte),
			HeightMap:        levmap["HeightMap"].([]byte),
			BlockLight:       levmap["BlockLight"].([]byte),
			Entities:         toEntityList(levmap["Entities"].([]interface{})),
			TileEntities:     levmap["TileEntities"].(interface{}),
			LastUpdate:       levmap["LastUpdate"].(int64),
			XPos:             levmap["xPos"].(int32),
			ZPos:             levmap["xPos"].(int32),
			TerrainPopulated: levmap["TerrainPopulated"].(int8),
		},
	}
}
func toEntityList(payload []interface{}) []*Entity {
	entities := make([]*Entity, len(payload))
	for i, e := range payload {
		entities[i] = toEntity(e.(map[string]interface{}))
	}
	return entities
}

func toEntity(payload map[string]interface{}) *Entity {
	xyz := payload["Pos"].([]interface{})       // FIXME
	dxdydz := payload["Motion"].([]interface{}) // FIXME
	rpy := payload["Rotation"].([]interface{})  // FIXME

	ent := Entity{
		Id:           payload["id"].(string),
		OnGround:     payload["OnGround"].(int8),
		Air:          payload["Air"].(int16),
		Fire:         payload["Fire"].(int16),
		FallDistance: payload["FallDistance"].(float32),
		Physics: Physics{
			Position{xyz[0].(float64), xyz[1].(float64), xyz[2].(float64)},
			Velocity{dxdydz[0].(float64), dxdydz[1].(float64), dxdydz[2].(float64)},
			Euler{0, rpy[1].(float32), rpy[0].(float32)},
		},
	}

	// nullables
	ihealth, ok := payload["Health"].(int16)
	if ok {
		ent.Health = &ihealth
	}

	iage, ok := payload["Age"].(int16)
	if ok {
		ent.Age = &iage
	}

	itile, ok := payload["Tile"].(int16)
	if ok {
		ent.Tile = &itile
	}

	iitem, ok := payload["Item"].(map[string]interface{})
	if ok {
		ent.Item = &Item{
			Id:     iitem["id"].(int16),
			Count:  iitem["Count"].(int8),
			Damage: iitem["Damage"].(int16),
		}
	}
	return &ent
}
