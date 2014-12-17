package main

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"image"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
)

type Rectangle struct {
	*image.RGBA
	EncodingType int32
}

type FramebufferUpdate struct {
	// How long to wait before this update should be applied
	Offset int64
	// The rectangles that should be updated
	Rectangles []Rectangle
}

type PixelFilter int

const (
	CopyFilter PixelFilter = iota
	PaletteFilter
	GradientFilter
)

type vncPixelFormat struct {
	BitsPerPixel, Depth, BigEndianFlag, TrueColorFlag uint8
	RedMax, GreenMax, BlueMax                         uint16
	RedShift, GreenShift, BlueShift                   uint8
	Padding                                           [3]byte
}

var (
	port = flag.Int("port", 7777, "port to listen for jobs on")
)

var pixelFormat = vncPixelFormat{
	BitsPerPixel:  0x20,
	Depth:         0x18,
	BigEndianFlag: 0x0,
	TrueColorFlag: 0x1,
	RedMax:        0xff,
	GreenMax:      0xff,
	BlueMax:       0xff,
	RedShift:      0x10,
	GreenShift:    0x8,
	BlueShift:     0x0,
}

func readFile(f http.File) (chan *FramebufferUpdate, error) {
	gzipReader, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	bufioReader := bufio.NewReader(gzipReader)
	reader := NewRecordingReader(bufioReader)
	output := make(chan *FramebufferUpdate)

	go func() {
		defer f.Close()
		defer close(output)

		var err error
		for err == nil {
			err = readUpdate(reader, output)
		}

		if err != nil && err != io.EOF {
			log.Println("error decoding recording:", err)
		}
	}()

	return output, nil
}

func readUpdate(reader *RecordingReader, output chan *FramebufferUpdate) error {
	// Decode the message type
	var mType uint8
	if err := binary.Read(reader, binary.BigEndian, &mType); err != nil {
		if err == io.EOF {
			return err // No more updates
		}
		return errors.New("unable to decode message type")
	}

	//log.Println("message type:", mType)

	// Skip message that aren't framebuffer updates
	if mType != 0 {
		return errors.New("unable to decode, found non-framebuffer update")
	}

	update := FramebufferUpdate{}
	update.Offset = reader.Offset()

	// Skip the one byte of padding
	if _, err := reader.Read(make([]byte, 1)); err != nil {
		return errors.New("unable to skip padding")
	}

	// Decode the number of rectangles
	var numRects uint16
	if err := binary.Read(reader, binary.BigEndian, &numRects); err != nil {
		return errors.New("unable to decode number of rectangles")
	}

	//log.Println("number of rectangles:", numRects)

	// Read all the rectangles
	for len(update.Rectangles) < int(numRects) {
		var err error

		rect, err := ReadRectangle(reader)
		if err != nil {
			return err
		}

		switch rect.EncodingType {
		case RawEncoding:
			err = DecodeRawEncoding(reader, &rect)
		case TightEncoding:
			err = DecodeTightEncoding(reader, &rect)
		case DesktopSize:
			err = DecodeDesktopSizeEncoding(reader, &rect)
		default:
			err = fmt.Errorf("unaccepted encoding: %d", rect.EncodingType)
		}

		if err != nil {
			return err
		}

		update.Rectangles = append(update.Rectangles, rect)
	}

	output <- &update
	return nil
}

func usage() {
	fmt.Printf("USAGE: %s [OPTION]... DIR\n", os.Args[0])
	flag.PrintDefaults()
}

func main() {
	flag.Usage = usage
	flag.Parse()

	if flag.NArg() != 1 {
		usage()
		os.Exit(1)
	}

	// Ensure that the first arg is an existent directory
	if fi, err := os.Stat(flag.Arg(0)); err != nil || !fi.IsDir() {
		fmt.Print("Invalid argument: must be an existent directory\n\n")
		usage()
		os.Exit(1)
	}

	addr := ":" + strconv.Itoa(*port)
	log.Printf("serving recordings from %s on %s", flag.Arg(0), addr)

	http.Handle("/", &playbackServer{http.Dir(flag.Arg(0))})
	http.ListenAndServe(addr, nil)
}
