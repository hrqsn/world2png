package main

/*
	level
	Copyright (c) 2019 beito
	This software is released under the MIT License.
	http://opensource.org/licenses/mit-license.php
*/

/*
	Thank you for this contribution!
	- @hrqsn
*/

import (
	_ "encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/urfave/cli/v2"
	"github.com/beito123/level"
	"github.com/beito123/level/block"
	"github.com/beito123/level/leveldb"
	"github.com/beito123/level/util"
	"github.com/cheggaaa/pb/v3"
	"github.com/pkg/errors"
)

func main() {
	app := &cli.App{
		Name: "world2png",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "world",
				Value: "./world",
				Usage: "ワールドのパス",
			},
			&cli.IntFlag{
				Name:  "scale",
				Value: 32,
				Usage: "生成するマップの大きさ",
			},
			&cli.IntFlag{
				Name:  "minx",
				Value: 0,
				Usage: "生成する範囲の最小値（X座標）",
			},
			&cli.IntFlag{
				Name:  "minz",
				Value: 0,
				Usage: "生成する範囲の最小値（Z座標）",
			},
		},
		Action: func(c *cli.Context) error {
			err := test(c.String("world"), c.Int("scale"), c.Int("minx"), c.Int("minz"))
			if err != nil {
				return err
			}
			return nil
		},
	}

	err := app.Run(os.Args)
	if err != nil {
		fmt.Printf("Error: %s", errors.WithStack(err))
	}
}

func test(_world string, _scale, _minx, _minz int) error {
	resPath := "./resources"
	texPath := "/vanilla/colors/"

	lvl, err := leveldb.Load(_world)
	if err != nil {
		return err
	}

	generator, err := NewMapGenerator(resPath+"/vanilla", lvl)
	if err != nil {
		return err
	}

	// For compatible with mcbe and mcje
	generator.Textures.AddAlias("minecraft:air", "minecraft:cave_air")
	generator.Textures.AddAlias("minecraft:grass_block", "minecraft:grass")

	generator.Textures.PathList["minecraft:granite"] = resPath + texPath + "stone_granite.png"
	generator.Textures.PathList["minecraft:diorite"] = resPath + texPath + "stone_diorite.png"
	generator.Textures.PathList["minecraft:andesite"] = resPath + texPath + "stone_andesite.png"
	generator.Textures.PathList["minecraft:lava"] = resPath + texPath + "lava_placeholder.png"
	generator.Textures.PathList["minecraft:water"] = resPath + texPath + "water_placeholder.png"
	generator.Textures.PathList["minecraft:grass"] = resPath + texPath + "grass_carried.png"
	generator.Textures.PathList["minecraft:grass_block"] = resPath + texPath + "grass_carried.png"

	// edges, _, err := lvl.LoadEdges()
	// if err != nil {
	// 	return err
	// }

	// minx, maxx, minz, maxz := edges[0], edges[1], edges[2], edges[3]
	// width, height := maxx-minx, maxz-minz

	scale := _scale
	line := 16 * scale
	img := image.NewRGBA(image.Rect(0, 0, line, line))

	bx := _minx
	by := _minz

	type ImageData struct {
		X     int
		Y     int
		Image image.Image
	}

	wg := new(sync.WaitGroup)

	imgCh := make(chan *ImageData, scale*scale)

	tmpl := `{{percent .}} {{ bar . "[" "=" ">" "_" "]"}} {{counters . }} {{speed . | rndcolor }}`
	bar := pb.ProgressBarTemplate(tmpl).Start(scale * scale)

	for i := 0; i < scale; i++ {
		for j := 0; j < scale; j++ {
			wg.Add(1)
			bar.Increment()

			go func(a, b int) {
				defer wg.Done()
				x := bx + a
				y := by + b

				gimg, err := generator.Generate(x, y)
				if err != nil {
					return
				}

				if gimg == nil { // not generated
					return
				}

				imgCh <- &ImageData{
					X:     a,
					Y:     b,
					Image: gimg,
				}
			}(i, j)
		}
	}

	wg.Wait()
	close(imgCh)

	for v := range imgCh {
		SetImage(v.Image, img, v.X*16, v.Y*16)
	}

	err = generator.Level.Close()
	if err != nil {
		return err
	}

	path := "./result/chunks.png"

	file, _ := os.Create(path)
	defer file.Close()

	err = png.Encode(file, img)
	if err != nil {
		return err
	}

	return nil
}

// NewMapGenerator returns new MapGenerator
// path is a dir path for offical resource pack
// rpath is a region dir path
func NewMapGenerator(path string, lvl level.Format) (*MapGenerator, error) {
	tm := NewTextureManager()

	err := tm.LoadResourcePack(path)
	if err != nil {
		return nil, err
	}

	return &MapGenerator{
		Level:    lvl,
		Textures: tm,
	}, nil
}

type MapGenerator struct {
	Level    level.Format
	Textures *TextureManager
}

// Generate generates a chunk image
// x and y are chunk coordinates
// if it's returned nil as Image, the chunk is not created
func (mg *MapGenerator) Generate(x, y int) (image.Image, error) {
	ok, err := mg.Level.HasGeneratedChunk(x, y)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, nil
	}

	chunk, err := mg.Level.Chunk(x, y)
	if err != nil {
		return nil, err
	}

	maker := ChunkImageMaker{}
	maker.Ready()

	ignoreBlocks := make(map[string]bool)

	for y := 0; y < 256; y++ {
		for z := 0; z < 16; z++ {
			for x := 0; x < 16; x++ {
				bl, err := chunk.GetBlock(x, y, z)
				if err != nil {
					return nil, err
				}

				var name string

				// For compatible
				b, ok := block.BlockListV112[bl.Name()]
				if ok {
					name = b.Name
				} else {
					name = bl.Name()
				}

				if name == "minecraft:air" || name == "minecraft:barrier" {
					continue
				}

				// Make palette
				_, ok = ignoreBlocks[name]
				if !ok && !maker.HasBlockData(name) { // if the block isn't registered
					if !mg.Textures.HasTexture(name) {
						ignoreBlocks[name] = true // register ignore list to avoid repeating

						//fmt.Printf("Ignore palette(name: %s)\n", name)
						continue
					}

					img, err := mg.Textures.GetTexture(name)
					if err != nil {
						return nil, fmt.Errorf("happened errors while processing palette(name: %s) error:%s", name, err)
					}

					maker.AddBlockData(name, img)
				}

				maker.Add(x, z, name)
			}
		}
	}

	return maker.Image, nil
}

var regCommentLine = regexp.MustCompile(`//.*\n`)

func NewTextureManager() *TextureManager {
	return &TextureManager{
		PathList:       make(map[string]string),
		Aliases:        make(map[string][]string),
		preparedImages: make(map[string]image.Image),
	}
}

// TextureManager control textures for blocks
type TextureManager struct {
	PathList map[string]string
	Aliases  map[string][]string

	preparedImages map[string]image.Image

	mutex sync.RWMutex
}

func (tm *TextureManager) getBlockName(name string) (string, bool) {
	tm.mutex.RLock()
	_, ok := tm.PathList[name]
	if ok {
		tm.mutex.RUnlock()
		return name, true
	}

	for n, v := range tm.Aliases {
		for _, c := range v {
			if c == name {
				tm.mutex.RUnlock()
				return n, true
			}
		}
	}

	tm.mutex.RUnlock()

	return "", false
}

func (tm *TextureManager) AddAlias(name string, aliases ...string) bool {
	tm.mutex.Lock()
	tm.Aliases[name] = append(tm.Aliases[name], aliases...)
	tm.mutex.Unlock()

	return true
}

func (tm *TextureManager) HasTexture(name string) bool {
	name, ok := tm.getBlockName(name)
	if !ok {
		return false
	}

	tm.mutex.RLock()

	path, ok := tm.PathList[name]
	if !ok {
		tm.mutex.RUnlock()
		return false
	}

	tm.mutex.RUnlock()

	if !util.ExistFile(path) {
		return false
	}

	return true
}

func (tm *TextureManager) GetTexture(name string) (image.Image, error) {
	if !tm.HasPrepared(name) {
		if !tm.HasTexture(name) {
			return nil, fmt.Errorf("couldn't find a image file")
		}

		err := tm.Prepare(name)
		if err != nil {
			return nil, err
		}
	}

	tm.mutex.RLock()
	result := tm.preparedImages[name]
	tm.mutex.RUnlock()

	return result, nil
}

func (tm *TextureManager) HasPrepared(name string) bool {
	name, ok := tm.getBlockName(name)
	if !ok {
		return false
	}
	tm.mutex.RLock()
	_, ok = tm.preparedImages[name]
	tm.mutex.RUnlock()

	return ok
}

func (tm *TextureManager) Prepare(name string) error {
	name, ok := tm.getBlockName(name)
	if !ok {
		return fmt.Errorf("couldn't find a block")
	}

	tm.mutex.RLock()
	path, ok := tm.PathList[name]
	if !ok {
		tm.mutex.RUnlock()
		return fmt.Errorf("couldn't find a path for the block")
	}

	tm.mutex.RUnlock()

	if !util.ExistFile(path) {
		return fmt.Errorf("couldn't find a image file")
	}

	file, err := os.Open(path)
	if err != nil {
		return err
	}

	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return err
	}

	tm.mutex.Lock()
	tm.preparedImages[name] = img
	tm.mutex.Unlock()

	return nil
}

func (tm *TextureManager) Load(list map[string]string) {
	for n, v := range list {
		tm.PathList[n] = v
	}
}

// LoadResourcePack loads textures from offical resource pack (you can download from https://www.minecraft.net/en-us/)
// path is a path for resource pack, you need to unzip in advance
func (tm *TextureManager) LoadResourcePack(path string) error {
	path = filepath.Clean(path)

	b, err := ioutil.ReadFile(path + "/blocks.json")
	if err != nil {
		return err
	}

	// bad hack for mojang // json isn't allowed comment lines (//)
	b = regCommentLine.ReplaceAll(b, []byte{}) // Remove comment lines

	// bad hack for mojang // you don't change type(string, array) for the same "textures"
	// I want to make struct...
	var data map[string]interface{}

	err = json.Unmarshal(b, &data)
	if err != nil {
		return err
	}

	tm.mutex.Lock()
	for name, d := range data { // of course, bad hack for mojang
		var tname string

		d2, ok := d.(map[string]interface{})
		if !ok {
			continue // ignore // it's format_version
		}

		switch ntype := d2["textures"].(type) {
		case map[string]interface{}:
			tname = ntype["up"].(string)
		case string:
			tname = ntype
		default:
			fmt.Printf("unknown: %s -> %#v\n", name, ntype)
			continue
		}

		tm.PathList["minecraft:"+name] = util.To(path, "/colors/"+tname+".png")
	}

	tm.mutex.Unlock()

	return nil
}

type ChunkImageMaker struct {
	Image *image.RGBA

	BlockList      map[string]image.Image
	FreeMap        []bool
	EnabledFreeMap bool
}

func (mk *ChunkImageMaker) Ready() {
	line := 16
	mk.Image = image.NewRGBA(image.Rect(0, 0, line, line))
	mk.BlockList = make(map[string]image.Image)

	mk.FreeMap = make([]bool, 16*16)
}

func (mk *ChunkImageMaker) Output(path string) error {
	file, _ := os.Create(path)
	defer file.Close()

	return png.Encode(file, mk.Image)
}

func (mk *ChunkImageMaker) IsFree(x, y int) bool {
	return !mk.FreeMap[y<<4|x]
}

func (mk *ChunkImageMaker) IsFull() bool {
	for _, v := range mk.FreeMap {
		if !v {
			return false
		}
	}

	return true
}

func (mk *ChunkImageMaker) Add(x, y int, name string) {
	block, ok := mk.BlockList[name]
	if !ok {
		return
	}

	if mk.EnabledFreeMap {
		mk.FreeMap[y<<4|x] = true
	}

	SetImage(block, mk.Image, x, y)

	return
}

func (mk *ChunkImageMaker) ResetBlockData() {
	mk.BlockList = make(map[string]image.Image)
}

func (mk *ChunkImageMaker) HasBlockData(name string) bool {
	_, ok := mk.BlockList[name]

	return ok
}

func (mk *ChunkImageMaker) AddBlockData(name string, img image.Image) {
	mk.BlockList[name] = img
}

func SetImage(src image.Image, dst *image.RGBA, atX, atY int) {
	/*for y := 0; y < src.Bounds().Dy(); y++ { // y
		for x := 0; x < src.Bounds().Dx(); x++ { // x
			dst.Set(atX+x, atY+y, src.At(x, y))
		}
	}*/

	size := src.Bounds().Size()
	rect := image.Rect(atX, atY, atX+size.X, atY+size.Y)
	draw.Draw(dst, rect, src, image.ZP, draw.Over)
}
